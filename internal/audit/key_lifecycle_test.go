package audit

import (
	"context"
	"testing"
	"time"
)

func TestAccessKeyEditExpiryRotationPreserveIdentityAndBudget(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, _ := configureBillingPolicy(t, s, 0)
	cfg := testInference(t, s, p)
	expiry := time.Now().Add(time.Hour)
	token, err := s.CreateKey(ctx, "admin", "rotating", []string{p.ID}, 60, &expiry)
	if err != nil {
		t.Fatal(err)
	}
	key, err := s.AuthenticateKey(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("INSERT INTO client_budgets(client_id,monthly_limit) VALUES($1,1000000000000)", key.ID); err != nil {
		t.Fatal(err)
	}
	l := AuditLog{ID: "key-lifecycle", ClientID: key.ID, Kind: "production", PolicyID: p.ID, InputStored: true}
	if err := s.Record(ctx, l, "historical input", 30); err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	reservation, err := s.ReserveCost(ctx, "rotated-cost", key.ID, "production", p, cfg, "historical input", false, at, l.ID, "channel")
	if err != nil {
		t.Fatal(err)
	}
	next, err := s.RotateKey(ctx, "admin", key.ID, key.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthenticateKey(ctx, token); errorCode(err) != "invalid_api_key" {
		t.Fatal("old key remained valid", err)
	}
	rotated, err := s.AuthenticateKey(ctx, next)
	if err != nil || rotated.ID != key.ID {
		t.Fatal("rotation changed caller", rotated, err)
	}
	rotated.Name = "updated"
	rotated.PolicyIDs = []string{}
	rotated.RPM = 120
	rotated.Active = false
	if err := s.UpdateKey(ctx, "admin", rotated, rotated.Revision); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateKey(ctx, "admin", rotated, rotated.Revision); err != ErrConflict {
		t.Fatal("stale update accepted", err)
	}
	rotated.Revision++
	rotated.Active = true
	rotated.PolicyIDs = []string{p.ID}
	past := time.Now().Add(-time.Minute)
	rotated.ExpiresAt = &past
	if err := s.UpdateKey(ctx, "admin", rotated, rotated.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthenticateKey(ctx, next); errorCode(err) != "invalid_api_key" {
		t.Fatal("expired key accepted", err)
	}
	if _, err := s.SettleCost(ctx, reservation, Usage{Attempted: true, Reported: true, PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}, at.Add(time.Second), nil); err != nil {
		t.Fatal("rotation interrupted existing settlement", err)
	}
	detail, err := s.LogDetail(ctx, l.ID)
	if err != nil || detail.Input != "historical input" || detail.ClientID != key.ID {
		t.Fatal("history lost", err)
	}
	keys, err := s.Keys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range keys {
		if k.ID == key.ID {
			if k.RotatedAt == nil || k.LastUsedAt == nil || k.ExpiresAt == nil || k.Name != "updated" || k.RPM != 120 {
				t.Fatal("key metadata incomplete", k)
			}
		}
	}
	var budgets int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM client_budgets WHERE client_id=$1", key.ID).Scan(&budgets); err != nil || budgets != 1 {
		t.Fatal("rotation reset budget", err)
	}
}
