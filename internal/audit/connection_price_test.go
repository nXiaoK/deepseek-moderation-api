package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestConnectionPriceOverrideAndResetPreserveSnapshots(t *testing.T) {
	s := testStore(t)
	p, key := configureBillingPolicy(t, s, 0)
	cfg := testInference(t, s, p)
	ctx := context.Background()
	app, err := NewServer(s, "http://localhost:8090", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("INSERT INTO admin_sessions(token_hash,username,csrf,expires_at) VALUES($1,'admin','csrf',NOW()+INTERVAL '1 hour')", digest("session")); err != nil {
		t.Fatal(err)
	}
	call := func(method, path string, body any, login bool, status int) {
		t.Helper()
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, bytes.NewReader(raw))
		r.Header.Set("Origin", app.Origin)
		if login {
			r.Header.Set("Cookie", "audit_session=session")
			r.Header.Set("X-CSRF-Token", "csrf")
		}
		w := httptest.NewRecorder()
		app.Handler().ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s: %d: %s", method, path, w.Code, w.Body.String())
		}
	}
	global, err := s.Price(ctx, cfg.Model, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"model": cfg.Model, "credential_id": cfg.CredentialID, "source": "connection tariff", "expected_price_id": 0, "rates_cny": map[string]string{"off_hit": "10", "off_miss": "10", "off_output": "10", "peak_hit": "10", "peak_miss": "10", "peak_output": "10"}}
	call("POST", "/admin/billing/prices", body, false, 401)
	call("POST", "/admin/billing/prices", body, true, 200)
	call("POST", "/admin/billing/prices", body, true, 409)
	card, err := s.ConnectionPrice(ctx, cfg.Model, cfg.CredentialID, time.Now())
	if err != nil || card == nil || card.CredentialID != cfg.CredentialID || card.ID == global.ID || card.Rates.OffMiss != 10000000 {
		t.Fatal(card, err)
	}
	other, err := s.ConnectionPrice(ctx, cfg.Model, "another-connection", time.Now())
	if err != nil || other == nil || other.ID != global.ID {
		t.Fatal("override crossed connections", other, err)
	}
	start := time.Now()
	entry, err := s.ReserveCost(ctx, "override-cost", key.ID, "production", p, cfg, "test", false, start)
	if err != nil || entry.Price.ID != card.ID {
		t.Fatal("reservation missed override", err)
	}
	path := "/admin/billing/prices/" + strconv.FormatInt(card.ID, 10)
	call("DELETE", path, nil, false, 401)
	call("DELETE", path, nil, true, 200)
	call("DELETE", path, nil, true, 409)
	fallback, err := s.ConnectionPrice(ctx, cfg.Model, cfg.CredentialID, time.Now())
	if err != nil || fallback.ID != global.ID {
		t.Fatal("default not restored", fallback, err)
	}
	cost, err := s.SettleCost(ctx, entry, Usage{Attempted: true, Reported: true, PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}, start.Add(time.Second))
	if err != nil || cost.AmountCNY == nil || *cost.AmountCNY != "0.00015" {
		t.Fatal("old reservation repriced", cost, err)
	}
	var snapshot PriceCard
	var raw []byte
	if err := s.DB.QueryRow("SELECT price_snapshot FROM audit_costs WHERE id='override-cost'").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &snapshot); err != nil || snapshot.ID != card.ID || snapshot.CredentialID != cfg.CredentialID {
		t.Fatal("snapshot lost", err)
	}
	call("POST", "/admin/billing/prices", body, true, 200)
	call("DELETE", "/admin/billing/prices/"+strconv.FormatInt(global.ID, 10), nil, true, 400)
}
