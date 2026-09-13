package audit

import (
	"context"
	"encoding/json"
	"math"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestAnalyticsRanges(t *testing.T) {
	now := time.Date(2026, 9, 13, 4, 0, 0, 0, time.UTC)
	for name, duration := range map[string]time.Duration{"1h": time.Hour, "24h": 24 * time.Hour, "48h": 48 * time.Hour, "7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour} {
		f, err := parseAnalyticsFilter(url.Values{"range": {name}}, now)
		if err != nil || f.To.Sub(f.From) != duration || f.Kind != "production" {
			t.Fatal(name, f, err)
		}
	}
	for _, query := range []string{"range=bad", "range=custom", "range=custom&from=bad&to=bad", "kind=other", "range=custom&from=2026-09-13T00:00:00Z&to=2026-09-12T00:00:00Z", "range=custom&from=2024-01-01T00:00:00Z&to=2026-01-01T00:00:00Z"} {
		q, _ := url.ParseQuery(query)
		if _, err := parseAnalyticsFilter(q, now); err == nil {
			t.Fatal("accepted invalid filter", query)
		}
	}
}

func TestModelAnalyticsAggregation(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	from := time.Date(2026, 9, 12, 16, 0, 0, 0, time.UTC)
	f := AnalyticsFilter{From: from, To: from.Add(24 * time.Hour), Kind: "production", IntervalSeconds: 3600}
	insert := func(id, model, kind, status string, offset time.Duration, usage Usage, amount any, latency any, sent any) {
		t.Helper()
		raw, _ := json.Marshal(usage)
		_, err := store.DB.ExecContext(ctx, `INSERT INTO audit_costs(id,request_id,channel_id,client_id,kind,policy_id,model,started_at,budget_date,reserved_pico,amount_pico,status,usage,latency_ms,request_sent) VALUES($1,'shared-retry-request','channel','client',$2,'policy',$3,$4,$4::timestamptz::date,0,$5,$6,$7,$8,$9)`, id, kind, model, from.Add(offset), amount, status, string(raw), latency, sent)
		if err != nil {
			t.Fatal(err)
		}
	}
	insert("a1", "configured-alias", "production", "calculated", 10*time.Minute, Usage{ActualModel: "model-a", Reported: true, PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150}, int64(1_000_000_000), 1000, true)
	insert("a2", "model-a", "production", "estimated", 70*time.Minute, Usage{Reported: true, PromptTokens: 200, CompletionTokens: 100, TotalTokens: 300}, int64(2_000_000_000), 2000, true)
	insert("cached", "model-a", "production", "local_cache", 80*time.Minute, Usage{Reported: true, TotalTokens: 999}, 0, nil, false)
	insert("b1", "model-b", "production", "pending", 90*time.Minute, Usage{}, nil, 3000, true)
	insert("legacy", "model-b", "production", "calculated", 2*time.Hour, Usage{Reported: true, CompletionTokens: 40, TotalTokens: 40}, int64(3_000_000_000), nil, nil)
	insert("legacy-missing", "model-b", "production", "calculated", 3*time.Hour, Usage{Reported: true, CompletionTokens: 10, TotalTokens: 10}, 0, nil, nil)
	insert("test", "model-a", "test", "calculated", time.Hour, Usage{Reported: true, TotalTokens: 700}, int64(7_000_000_000), 1000, true)
	insert("end-exclusive", "model-a", "production", "calculated", 24*time.Hour, Usage{Reported: true, TotalTokens: 900}, int64(9_000_000_000), 1000, true)
	log := AuditLog{ID: "shared-retry-request", Attempts: []AuditAttempt{{ID: "legacy", Sent: true, LatencyMS: 4000}}, CreatedAt: from}
	if err := store.Record(ctx, log, "", 30); err != nil {
		t.Fatal(err)
	}
	result, err := store.Analytics(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	m := result.Summary
	if m.Records != 6 || m.Calls != 5 || m.CacheHits != 1 || m.InputTokens != 300 || m.OutputTokens != 200 || m.TotalTokens != 500 || m.UnknownUsage != 1 || m.KnownCostCNY != "0.006" || m.EstimatedCostCNY != "0.002" || m.PendingCosts != 1 {
		t.Fatalf("incorrect totals: %+v", m)
	}
	if m.LatencySamples != 4 || m.AvgLatencyMS == nil || *m.AvgLatencyMS != 2500 || m.OutputTokensPerSecond == nil || math.Abs(*m.OutputTokensPerSecond-190.0/7) > 0.0001 {
		t.Fatalf("incorrect speed sample accounting: %+v", m)
	}
	if len(result.Models) != 2 || len(result.Series) != 24 || result.Series[0].Time != from || result.Series[23].Calls != 0 {
		t.Fatalf("incorrect grouping/buckets: %+v", result)
	}
	f.Model = "model-a"
	filtered, err := store.Analytics(ctx, f)
	if err != nil || filtered.Summary.Calls != 2 || filtered.Summary.TotalTokens != 450 || len(filtered.AvailableModels) != 2 || len(filtered.Models) != 1 {
		t.Fatal("model filter failed", filtered, err)
	}
	f.Model = "model-a' OR 1=1 --"
	empty, err := store.Analytics(ctx, f)
	if err != nil || empty.Summary.Records != 0 || empty.Summary.AvgLatencyMS != nil || len(empty.Models) != 0 {
		t.Fatal("empty/filter query failed", empty, err)
	}
	f.Model = ""
	f.Kind = "all"
	all, err := store.Analytics(ctx, f)
	if err != nil || all.Summary.TotalTokens != 1200 {
		t.Fatal("kind filter failed", all, err)
	}
	// Reconciliation updates the report without rewriting request snapshots.
	if _, err := store.DB.ExecContext(ctx, "UPDATE audit_costs SET amount_pico=4000000000,status='reconciled' WHERE id='b1'"); err != nil {
		t.Fatal(err)
	}
	f.Kind = "production"
	updated, err := store.Analytics(ctx, f)
	if err != nil || updated.Summary.KnownCostCNY != "0.01" || updated.Summary.PendingCosts != 0 {
		t.Fatal("reconciliation not reflected", updated, err)
	}
}

func TestAnalyticsRequiresAdmin(t *testing.T) {
	store := testStore(t)
	app, err := NewServer(store, "http://localhost:8090", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	app.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/admin/analytics?range=24h", nil))
	if w.Code != 401 {
		t.Fatal("analytics accessible without admin", w.Code)
	}
}

func TestCostSettlementKeepsAnalyticsTiming(t *testing.T) {
	store := testStore(t)
	p, key := configureBillingPolicy(t, store, 0)
	cfg := testInference(t, store, p)
	ctx := context.Background()
	start := time.Now()
	entry, err := store.ReserveCost(ctx, randomToken("cost_"), key.ID, "production", p, cfg, "hello", false, start)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SettleCost(ctx, entry, Usage{Attempted: true, Reported: true, PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}, start.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	f := AnalyticsFilter{From: start.Add(-time.Second), To: start.Add(3 * time.Second), Kind: "production", IntervalSeconds: 60}
	result, err := store.Analytics(ctx, f)
	if err != nil || result.Summary.AvgLatencyMS == nil || *result.Summary.AvgLatencyMS != 2000 {
		t.Fatal("timing was not persisted independently of audit retention", result, err)
	}
}
