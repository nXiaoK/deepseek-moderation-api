package audit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEditCredentialAPIAddress(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	t.Setenv("AUDIT_SUB2API_ORIGINS", "https://old.test,https://new.test")
	for _, provider := range []string{ProviderDeepSeek, ProviderGrok} {
		t.Run(provider, func(t *testing.T) {
			id, err := store.SaveProviderCredential(ctx, "admin", "", "editable", "original-test-key", true, provider, "https://old.test")
			if err != nil {
				t.Fatal(err)
			}
			channel := createTestChannel(t, store, id, "editable", "test-model")
			cfg := DefaultConfig()
			cfg.Provider, cfg.CredentialID, cfg.BaseURL = provider, id, "https://old.test"
			for _, update := range []struct{ base, key, wantKey string }{
				{"https://old.test/custom/api/", "", "original-test-key"},
				{"https://new.test/proxy/v1", "replacement-test-key", "replacement-test-key"},
				{"https://new.test/", "", "replacement-test-key"},
			} {
				if got, err := store.SaveProviderCredential(ctx, "admin", id, "edited", update.key, true, provider, update.base); err != nil || got != id {
					t.Fatal("edit failed", got, err)
				}
				if _, err := store.CredentialForConfig(ctx, cfg); errorCode(err) != "credential_provider_mismatch" {
					t.Fatal("stale endpoint still accepted", err)
				}
				cfg.BaseURL = normalizeProviderURL(update.base)
				if key, err := store.CredentialForConfig(ctx, cfg); err != nil || key != update.wantKey {
					t.Fatal("updated endpoint or secret incorrect", err)
				}
				channels, err := store.Channels(ctx)
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, current := range channels {
					if current.ID == channel.ID {
						found = true
						if current.BaseURL != cfg.BaseURL || current.Revision != channel.Revision+1 || current.CacheEpoch == channel.CacheEpoch {
							t.Fatal("channel endpoint, revision or cache epoch not refreshed", current)
						}
						channel = current
					}
				}
				if !found {
					t.Fatal("bound channel missing")
				}
			}
			invalid := []string{"https://user:secret@new.test/api", "https://new.test/api?key=secret", "https://new.test/api#fragment"}
			if provider == ProviderGrok {
				invalid = append(invalid, "https://unapproved.test/api")
			}
			for _, base := range invalid {
				if _, err := store.SaveProviderCredential(ctx, "admin", id, "invalid", "", true, provider, base); errorCode(err) != "invalid_connection" {
					t.Fatal("invalid edit accepted", base, err)
				}
			}
			if key, err := store.CredentialForConfig(ctx, cfg); err != nil || key != "replacement-test-key" {
				t.Fatal("rejected edit changed credential", err)
			}
		})
	}
}

func TestDeleteCredentialAuthorizationAndLifecycle(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	app, err := NewServer(store, "http://localhost:8090", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	token, csrf := randomToken("session_"), randomToken("csrf_")
	if _, err := store.DB.ExecContext(ctx, `INSERT INTO admin_sessions(token_hash,username,csrf,expires_at) VALUES($1,'admin',$2,NOW()+INTERVAL '1 hour')`, digest(token), csrf); err != nil {
		t.Fatal(err)
	}
	other, err := store.SaveCredential(ctx, "admin", "", "keep", "test-only-keep-secret", true)
	if err != nil {
		t.Fatal(err)
	}
	handler := app.Handler()
	for _, active := range []bool{true, false} {
		id, err := store.SaveCredential(ctx, "admin", "", "delete", "test-only-delete-secret", active)
		if err != nil {
			t.Fatal(err)
		}
		call := func(login, validCSRF bool, want int) {
			t.Helper()
			r := httptest.NewRequest("DELETE", "/admin/credentials/"+id, nil)
			r.Header.Set("Origin", app.Origin)
			if login {
				r.AddCookie(&http.Cookie{Name: "audit_session", Value: token})
			}
			if validCSRF {
				r.Header.Set("X-CSRF-Token", csrf)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != want {
				t.Fatalf("delete: got %d, want %d: %s", w.Code, want, w.Body.String())
			}
		}
		call(false, false, 401)
		call(true, false, 403)
		items, err := store.Credentials(ctx)
		if err != nil || len(items) != 2 {
			t.Fatalf("rejected delete changed credentials: %+v, %v", items, err)
		}
		call(true, true, 200)
		call(true, true, 404)
		items, err = store.Credentials(ctx)
		if err != nil || len(items) != 1 || items[0].ID != other {
			t.Fatalf("wrong remaining credentials: %+v, %v", items, err)
		}
		if _, err := store.CredentialSecret(ctx, id); errorCode(err) != "credential_unavailable" {
			t.Fatalf("deleted credential usable: %v", err)
		}
		var rows, actions int
		if err := store.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM provider_credentials WHERE id=$1", id).Scan(&rows); err != nil || rows != 0 {
			t.Fatalf("credential ciphertext retained: %d, %v", rows, err)
		}
		if err := store.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM admin_action_logs WHERE action='credential.delete' AND resource_id=$1", id).Scan(&actions); err != nil || actions != 1 {
			t.Fatalf("wrong delete action count: %d, %v", actions, err)
		}
		if err := store.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM admin_action_logs WHERE action='credential.update' AND resource_id=$1", id).Scan(&actions); err != nil || actions != 1 {
			t.Fatalf("previous action lost: %d, %v", actions, err)
		}
	}
}

func TestDeleteCredentialRejectsChannelReferences(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	replacement, err := store.SaveCredential(ctx, "admin", "", "replacement", "test-replacement-secret", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []bool{true, false} {
		id, err := store.SaveCredential(ctx, "admin", "", "referenced", "test-referenced-secret", true)
		if err != nil {
			t.Fatal(err)
		}
		c, err := store.SaveChannel(ctx, "admin", ModelChannel{Name: id, Model: "deepseek-flash", CredentialID: id, TimeoutMS: 4000, MaxTokens: 512, MaxConcurrency: 8, Enabled: enabled})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.DeleteCredential(ctx, "admin", id); errorCode(err) != "credential_in_use" || !strings.Contains(err.Error(), "审核模型") {
			t.Fatalf("reference was not rejected: %v", err)
		}
		if _, err := store.CredentialSecret(ctx, id); err != nil {
			t.Fatal("rejected delete damaged credential", err)
		}
		var actions int
		if err := store.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM admin_action_logs WHERE action='credential.delete' AND resource_id=$1", id).Scan(&actions); err != nil || actions != 0 {
			t.Fatalf("rejected delete logged as successful: %d, %v", actions, err)
		}
		c.CredentialID = replacement
		if _, err := store.SaveChannel(ctx, "admin", c); err != nil {
			t.Fatal(err)
		}
		if err := store.DeleteCredential(ctx, "admin", id); err != nil {
			t.Fatal("unbound credential could not be deleted", err)
		}
	}
}
