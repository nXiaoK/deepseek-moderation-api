package audit

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestGenericPriceMatchingPrecedenceAndAvailability(t *testing.T) {
	s := testStore(t)
	p, _ := configureBillingPolicy(t, s, 0)
	credential := testInference(t, s, p).CredentialID
	ctx := context.Background()
	at := time.Now()
	insert := func(model, scope string, active bool, effective time.Time) int64 {
		t.Helper()
		raw, _ := json.Marshal(PriceRates{OffHit: 1000000, OffMiss: 1000000, OffOutput: 1000000, PeakHit: 1000000, PeakMiss: 1000000, PeakOutput: 1000000})
		var id int64
		if err := s.DB.QueryRow(`INSERT INTO model_prices(model,credential_id,rates,source,author,active,effective_at) VALUES($1,$2,$3,'test','admin',$4,$5) RETURNING id`, model, scope, string(raw), active, effective).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	check := func(model, scope string, want int64) {
		t.Helper()
		card, err := s.ConnectionPrice(ctx, model, scope, at)
		if err != nil {
			t.Fatal(err)
		}
		if want == 0 && card != nil || want != 0 && (card == nil || card.ID != want) {
			t.Fatalf("%s / %s: got %+v, want price %d", model, scope, card, want)
		}
		_, prices, err := s.routeResources(ctx, []ModelChannel{{Model: model, CredentialID: scope}}, true, at)
		if err != nil || prices[routePriceKey{canonicalPriceModel(model), scope}] != (want != 0) {
			t.Fatal("routing disagrees with pricing", model, prices, err)
		}
	}
	base := insert("deepseek", "", true, at.Add(-time.Hour))
	check("deepseek-v4-flush", credential, base)
	check("vendor/DEEPSEEK-v4-flush", credential, base)
	check("unknown-model", credential, 0)
	connection := insert("deepseek", credential, true, at.Add(-time.Minute))
	check("deepseek-v4-flush", credential, connection)
	check("deepseek-v4-flush", "", base)
	specific := insert("deepseek-v4", "", true, at.Add(-time.Hour))
	check("deepseek-v4-flush", credential, specific)
	exact := insert("deepseek-v4-flush", "", true, at.Add(-time.Hour))
	check("deepseek-v4-flush", credential, exact)
	check("DEEPSEEK-V4-FLUSH", credential, exact)
	override := insert("deepseek-v4-flush", credential, true, at.Add(-time.Minute))
	check("deepseek-v4-flush", credential, override)
	insert("deepseek-v4-flush", credential, false, at.Add(-time.Second))
	check("deepseek-v4-flush", credential, exact)
	insert("deepseek-v4-flush", "", true, at.Add(time.Hour))
	check("deepseek-v4-flush", credential, exact)
	old, err := s.ConnectionPrice(ctx, "deepseek-v4-flush", credential, at.Add(-2*time.Hour))
	if err != nil || old != nil {
		t.Fatal("future price leaked into history", old, err)
	}
	insert("reset-only", credential, true, at.Add(-time.Minute))
	insert("reset-only", credential, false, at.Add(-time.Second))
	check("reset-only-v1", credential, 0)
	literal := insert("wild_model", "", true, at.Add(-time.Second))
	check("wild_model-v1", credential, literal)
	check("wildXmodel-v1", credential, 0)
	insert("abc", "", true, at.Add(-time.Minute))
	tie := insert("bcd", "", true, at.Add(-time.Second))
	check("abcd", credential, tie)
	alias, err := s.Price(ctx, "deepseek-flash", at)
	if err != nil || alias == nil {
		t.Fatal(alias, err)
	}
	check("deepseek-v4-flash", credential, alias.ID)
	check("DEEPSEEK-V4-FLASH", credential, alias.ID)
}

func TestGenericPriceBillingBudgetEvaluationAndSnapshots(t *testing.T) {
	app, p, call := evaluationTestServer(t)
	s := app.Store
	ctx := context.Background()
	channels, err := s.Channels(ctx)
	if err != nil || len(channels) != 1 {
		t.Fatal(channels, err)
	}
	channel := channels[0]
	channel.Model = "deepseek-v4-flush"
	channel, err = s.SaveChannel(ctx, "admin", channel)
	if err != nil {
		t.Fatal(err)
	}
	p, channels, err = s.RouteSnapshot(ctx, p.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := channel.Inference(p.Config)
	token, err := s.CreateKey(ctx, "admin", "generic pricing", []string{p.ID}, 60)
	if err != nil {
		t.Fatal(err)
	}
	key, err := s.AuthenticateKey(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("INSERT INTO client_budgets(client_id,daily_limit,monthly_limit) VALUES($1,$2,$2)", key.ID, 10*picoPerCNY); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"model": "deepseek", "source": "generic tariff", "expected_price_id": 0, "rates_cny": map[string]string{"off_hit": "1", "off_miss": "1", "off_output": "1", "peak_hit": "1", "peak_miss": "1", "peak_output": "1"}}
	call("POST", "/admin/billing/prices", body, 200)
	card, err := s.ConnectionPrice(ctx, cfg.Model, cfg.CredentialID, time.Now())
	if err != nil || card == nil || card.Model != "deepseek" {
		t.Fatal(card, err)
	}
	estimate, err := app.estimateEvaluationTrial(ctx, evaluationPlan{Policy: p, Channels: channels}, channel.ID, "hello")
	want, reserveErr := card.reserve(cfg, "hello")
	if err != nil || reserveErr != nil || estimate != want {
		t.Fatal(estimate, want, err, reserveErr)
	}
	entry, err := s.ReserveCost(ctx, "generic-snapshot", key.ID, "production", p, cfg, "hello", false, time.Now())
	if err != nil || entry.Price.ID != card.ID {
		t.Fatal(entry, err)
	}
	var calls atomic.Int32
	app.Engine.Client = &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(cachedTestResponse))}, nil
	})}
	response, err := app.runAudit(ctx, p, channels, key.ID, "production", "hello", "")
	if err != nil || response.Cost == nil || response.Cost.AmountCNY == nil || *response.Cost.AmountCNY != "0.000015" || calls.Load() != 1 {
		t.Fatal(response, err, calls.Load())
	}
	// A later exact tariff must not change an existing generic reservation.
	body["model"] = cfg.Model
	body["rates_cny"] = map[string]string{"off_hit": "2", "off_miss": "2", "off_output": "2", "peak_hit": "2", "peak_miss": "2", "peak_output": "2"}
	call("POST", "/admin/billing/prices", body, 200)
	cost, err := s.SettleCost(ctx, entry, Usage{Attempted: true, Reported: true, PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}, time.Now())
	if err != nil || cost.AmountCNY == nil || *cost.AmountCNY != "0.000015" {
		t.Fatal(cost, err)
	}
	var snapshot PriceCard
	var raw []byte
	if err := s.DB.QueryRow("SELECT price_snapshot FROM audit_costs WHERE id=$1", entry.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &snapshot); err != nil || snapshot.ID != card.ID || snapshot.Model != "deepseek" {
		t.Fatal(snapshot, err)
	}
	if _, err := s.DB.Exec("UPDATE client_budgets SET daily_limit=0,monthly_limit=0 WHERE client_id=$1", key.ID); err != nil {
		t.Fatal(err)
	}
	_, err = app.runAudit(ctx, p, channels, key.ID, "production", "hello", "")
	if err == nil || errorCode(err) != "budget_exceeded" || calls.Load() != 1 {
		t.Fatal("generic tariff bypassed budget", err, calls.Load())
	}
	var sample EvaluationSample
	_ = json.Unmarshal(call("POST", "/admin/evaluation/samples", map[string]string{"name": "priced", "input": "hello", "expected": "allow"}, 200), &sample)
	var created map[string]string
	_ = json.Unmarshal(call("POST", "/admin/evaluation/runs", map[string]any{"name": "priced", "policy_id": p.ID, "sample_ids": []string{sample.ID}, "channel_ids": []string{channel.ID}, "repetitions": 1, "max_cost_cny": "5"}, 201), &created)
	run := waitEvaluation(t, app, created["id"])
	results, err := s.evaluationResults(ctx, run.ID)
	if err != nil || run.Status != "completed" || len(results) != 1 || results[0].Cost == nil || results[0].Cost.AmountCNY == nil || *results[0].Cost.AmountCNY != "0.00003" {
		t.Fatal(run, results, err)
	}
}
