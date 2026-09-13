package audit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDeleteAccessKeyPreservesHistoryAndSettlement(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	p, otherKey := configureBillingPolicy(t, store, 0)
	cfg := testInference(t, store, p)
	app, err := NewServer(store, "http://localhost:8090", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sessionToken, csrf := randomToken("session_"), randomToken("csrf_")
	if _, err := store.DB.ExecContext(ctx, `INSERT INTO admin_sessions(token_hash,username,csrf,expires_at) VALUES($1,'admin',$2,NOW()+INTERVAL '1 hour')`, digest(sessionToken), csrf); err != nil {
		t.Fatal(err)
	}
	handler := app.Handler()
	call := func(method, path, body, token string, login, validCSRF bool, want int) {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Origin", app.Origin)
		if login {
			r.AddCookie(&http.Cookie{Name: "audit_session", Value: sessionToken})
		}
		if validCSRF {
			r.Header.Set("X-CSRF-Token", csrf)
		}
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: got %d, want %d: %s", method, path, w.Code, want, w.Body.String())
		}
	}
	for _, revoked := range []bool{false, true} {
		name := "active"
		if revoked {
			name = "revoked"
		}
		t.Run(name, func(t *testing.T) {
			token, err := store.CreateKey(ctx, "admin", "delete "+name, []string{p.ID}, 60)
			if err != nil {
				t.Fatal(err)
			}
			key, err := store.AuthenticateKey(ctx, token)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.DB.ExecContext(ctx, `INSERT INTO client_budgets(client_id,daily_limit) VALUES($1,1000000000000)`, key.ID); err != nil {
				t.Fatal(err)
			}
			at := time.Now()
			log := AuditLog{ID: randomToken("audit_"), ClientID: key.ID, PolicyID: p.ID, Kind: "production", InputStored: true, CreatedAt: at}
			if err := store.Record(ctx, log, "historical input", 30); err != nil {
				t.Fatal(err)
			}
			pending, err := store.ReserveCost(ctx, randomToken("cost_"), key.ID, "production", p, cfg, "historical input", false, at, log.ID)
			if err != nil {
				t.Fatal(err)
			}
			if revoked {
				if err := store.RevokeKey(ctx, "admin", key.ID); err != nil {
					t.Fatal(err)
				}
			}
			path := "/admin/api-keys/" + key.ID
			call("DELETE", path, "", "", false, false, 401)
			call("DELETE", path, "", "", true, false, 403)
			if !revoked {
				if _, err := store.AuthenticateKey(ctx, token); err != nil {
					t.Fatal("rejected delete invalidated key")
				}
			}
			call("DELETE", path, "", "", true, true, 200)
			call("DELETE", path, "", "", true, true, 404)
			call("POST", path+"/revoke", "{}", "", true, true, 404)
			call("PUT", path+"/budget", `{"daily_limit_cny":"1","monthly_limit_cny":"1","expected_revision":1}`, "", true, true, 404)
			call("GET", "/v1/models", "", token, false, false, 401)
			call("POST", "/v1/moderations", `{"model":"abuse-audit-v1","input":"hello"}`, token, false, false, 401)
			keys, err := store.Keys(ctx)
			if err != nil || len(keys) != 1 || keys[0].ID != otherKey.ID {
				t.Fatalf("deleted key remains or another key was affected: %+v, %v", keys, err)
			}
			budgets, err := store.Budgets(ctx, at)
			if err != nil || len(budgets) != 1 || budgets[0].ClientID != otherKey.ID {
				t.Fatalf("deleted key remains in editable budgets: %+v, %v", budgets, err)
			}
			retained, err := store.LogDetail(ctx, log.ID)
			if err != nil || retained.ClientID != key.ID || retained.Input != "historical input" {
				t.Fatal("historical audit input was lost", err)
			}
			// A request already sent to the model must still be able to settle.
			if _, err := store.SettleCost(ctx, pending, Usage{Attempted: true, Reported: true, PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}, at); err != nil {
				t.Fatal("deletion broke in-flight cost settlement", err)
			}
			if cost, err := store.Cost(ctx, pending.ID); err != nil || cost.AmountCNY == nil {
				t.Fatal("historical cost was lost", err)
			}
			var actions int
			if err := store.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM admin_action_logs WHERE action='key.delete' AND resource_id=$1`, key.ID).Scan(&actions); err != nil || actions != 1 {
				t.Fatalf("delete must have exactly one action record: %d, %v", actions, err)
			}
			var invalidated bool
			if err := store.DB.QueryRowContext(ctx, `SELECT NOT active AND deleted_at IS NOT NULL AND token_hash<>$2 FROM client_api_keys WHERE id=$1`, key.ID, digest(token)).Scan(&invalidated); err != nil || !invalidated {
				t.Fatal("token verifier was not removed", err)
			}
		})
	}
}
