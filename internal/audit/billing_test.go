package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func configureBillingPolicy(t *testing.T, store *Store, cacheTTL int) (Policy, ClientKey) {
	t.Helper()
	ctx := context.Background()
	cred, err := store.SaveCredential(ctx, "admin", "", "test", "test-only-model-key", true)
	if err != nil {
		t.Fatal(err)
	}
	p, err := store.Policy(ctx, "abuse-default")
	if err != nil {
		t.Fatal(err)
	}
	c := createTestChannel(t, store, cred, "billing", "deepseek-flash")
	p.Config.Channels = []ChannelBinding{{c.ID, 1, 100, true}}
	p.Config.ResultCacheTTL = cacheTTL
	if err = store.SaveConfig(ctx, "admin", p.ID, p.Name, p.Revision, p.Config); err != nil {
		t.Fatal(err)
	}
	p, _ = store.Policy(ctx, p.ID)
	if err = store.SetPolicyState(ctx, "admin", p.ID, p.Revision, true); err != nil {
		t.Fatal(err)
	}
	p, _ = store.Policy(ctx, p.ID)
	token, err := store.CreateKey(ctx, "admin", "test caller", []string{p.ID}, 60)
	if err != nil {
		t.Fatal(err)
	}
	key, err := store.AuthenticateKey(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	return p, key
}
func TestConcurrentBudgetReservationPendingAndReconciliation(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	p, key := configureBillingPolicy(t, store, 0)
	cfg := testInference(t, store, p)
	now := time.Now()
	price, err := store.Price(ctx, cfg.Model, now)
	if err != nil || price == nil {
		t.Fatal("price missing", err)
	}
	reservation, _ := price.reserve(cfg, "hello")
	if _, err = store.DB.Exec("INSERT INTO client_budgets(client_id,daily_limit,monthly_limit) VALUES($1,$2,$2)", key.ID, reservation); err != nil {
		t.Fatal(err)
	}
	entries := make(chan *CostReservation, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			entry, e := store.ReserveCost(ctx, randomToken("cost_"), key.ID, "production", p, cfg, "hello", false, now)
			entries <- entry
			errs <- e
		}()
	}
	first, second := <-entries, <-entries
	e1, e2 := <-errs, <-errs
	if (e1 == nil) == (e2 == nil) {
		t.Fatalf("one reservation must win: %v %v", e1, e2)
	}
	entry := first
	if entry == nil {
		entry = second
	}
	pending, err := store.SettleCost(ctx, entry, Usage{Attempted: true}, now)
	if err != nil || pending.Status != "pending" || pending.AmountCNY != nil || pending.ReservedCNY != picoString(reservation) {
		t.Fatalf("unknown usage released funds: %+v %v", pending, err)
	}
	if _, err = store.ReserveCost(ctx, randomToken("cost_"), key.ID, "production", p, cfg, "hello", false, now); err == nil {
		t.Fatal("pending hold was ignored")
	}
	app, err := NewServer(store, "http://localhost:8090", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/", bytes.NewBufferString(`{"amount_cny":"0","reason":"test supplier confirmed no charge"}`))
	req.SetPathValue("id", entry.ID)
	req = req.WithContext(context.WithValue(req.Context(), sessionKey{}, session{Username: "admin"}))
	w := httptest.NewRecorder()
	if err = app.reconcileCost(w, req); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ReserveCost(ctx, randomToken("cost_"), key.ID, "production", p, cfg, "hello", false, now); err != nil {
		t.Fatal("reconciliation did not release hold", err)
	}
	var adjustments int
	if err = store.DB.QueryRow("SELECT COUNT(*) FROM cost_adjustments WHERE cost_id=$1", entry.ID).Scan(&adjustments); err != nil || adjustments != 1 {
		t.Fatal("missing reconciliation audit trail", err)
	}
	if err = app.reconcileCost(httptest.NewRecorder(), req); err == nil {
		t.Fatal("repeat reconciliation accepted")
	}
}
func TestCostSnapshotSettlementIdempotencyAndPeriodBudgets(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	p, key := configureBillingPolicy(t, store, 0)
	cfg := testInference(t, store, p)
	now := time.Now()
	entry, err := store.ReserveCost(ctx, randomToken("cost_"), key.ID, "production", p, cfg, "hello", false, now)
	if err != nil {
		t.Fatal(err)
	}
	rates := entry.Price.Rates
	rates.OffMiss *= 5
	rates.PeakMiss *= 5
	raw, _ := json.Marshal(rates)
	if _, err = store.DB.Exec("INSERT INTO model_prices(model,rates,source,author) VALUES($1,$2,'test update','admin')", cfg.Model, string(raw)); err != nil {
		t.Fatal(err)
	}
	usage := Usage{PromptTokens: 100, CompletionTokens: 10, TotalTokens: 110, CacheHitTokens: intPtr(80), CacheMissTokens: intPtr(20), Reported: true, Attempted: true}
	want, _, _, _ := entry.Price.calculate(usage, now, now)
	cost, err := store.SettleCost(ctx, entry, usage, now)
	if err != nil || cost.AmountCNY == nil || *cost.AmountCNY != picoString(want) || *cost.PriceID != entry.Price.ID {
		t.Fatal("cost did not preserve price snapshot", cost, err)
	}
	repeated, err := store.SettleCost(ctx, entry, Usage{}, now)
	if err != nil || repeated.AmountCNY == nil || *repeated.AmountCNY != *cost.AmountCNY {
		t.Fatal("settlement applied twice")
	}
	old := now.In(shanghai).AddDate(0, -1, 0)
	_, err = store.DB.Exec(`INSERT INTO audit_costs(id,client_id,kind,policy_id,request_id,model,started_at,budget_date,reserved_pico,amount_pico,status) VALUES('old-month',$1,'production',$2,'old-month',$3,$4,$5,0,1000000000000,'reconciled')`, key.ID, p.ID, cfg.Model, old, old.Format("2006-01-02"))
	if err != nil {
		t.Fatal(err)
	}
	views, err := store.Budgets(ctx, now)
	if err != nil || len(views) != 1 || views[0].MonthlyUsed != picoString(want) {
		t.Fatal("previous month charged to current budget", views, err)
	}
	// Prompt-log expiry never removes cost evidence needed for budget accounting.
	if err = store.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	cost, err = store.Cost(ctx, entry.ID)
	if err != nil || cost == nil {
		t.Fatal("cost evidence deleted")
	}
}

func TestConfiguredModelPriceAcrossProviders(t *testing.T) {
	for _, tc := range []struct {
		name, provider, base string
	}{
		{"official", ProviderDeepSeek, "https://api.deepseek.com"},
		{"third_party", ProviderDeepSeek, "https://gateway.test"},
		{"sub2api", ProviderGrok, "https://sub2api.test"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := testStore(t)
			ctx := context.Background()
			t.Setenv("AUDIT_SUB2API_ORIGINS", "https://sub2api.test")
			p, key := configureBillingPolicy(t, store, 0)
			cred, err := store.SaveProviderCredential(ctx, "admin", "", tc.name, "test-model-key", true, tc.provider, tc.base)
			if err != nil {
				t.Fatal(err)
			}
			channel := createTestChannel(t, store, cred, "display-name", "gpt-5.6-luna")
			p.Config.Channels = []ChannelBinding{{channel.ID, 1, 100, true}}
			if err = store.SaveConfig(ctx, "admin", p.ID, p.Name, p.Revision, p.Config); err != nil {
				t.Fatal(err)
			}
			p, err = store.Policy(ctx, p.ID)
			if err != nil {
				t.Fatal(err)
			}
			app, err := NewServer(store, "http://localhost:8090", t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			app.Engine.Client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
				var payload struct{ Model string }
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.Model != channel.Model {
					t.Fatal("wrong configured model", payload, err)
				}
				calls.Add(1)
				if tc.provider == ProviderGrok {
					return grokStreamResponse(grokSSE(`{"confidence":0.1,"reason":"ok"}`, `{"input_tokens":10,"output_tokens":5,"total_tokens":15,"input_tokens_details":{"cached_tokens":8}}`)), nil
				}
				body := `{"model":"upstream-model","choices":[{"finish_reason":"stop","message":{"content":"{\"confidence\":0.1,\"reason\":\"ok\"}"}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"prompt_cache_hit_tokens":8,"prompt_cache_miss_tokens":2}}`
				return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewBufferString(body))}, nil
			})}
			// A trial without a matching price stays pending after a price is published.
			if result, err := runTestAudit(t, app, p, "admin", "test", "before price"); err != nil || result.Cost.Status != "pending" || result.Cost.AmountCNY != nil {
				t.Fatal("unpriced request must remain unknown", result, err)
			}
			if _, err = store.DB.Exec("INSERT INTO client_budgets(client_id,monthly_limit) VALUES($1,1000000000000)", key.ID); err != nil {
				t.Fatal(err)
			}
			if _, err = runTestAudit(t, app, p, key.ID, "production", "before price"); errorCode(err) != "pricing_unavailable" || calls.Load() != 1 {
				t.Fatal("unpriced channel bypassed budget", err)
			}
			save := func(expectedID int64, output string) {
				t.Helper()
				raw, _ := json.Marshal(map[string]any{
					"model": channel.Model, "source": "test custom tariff", "expected_price_id": expectedID,
					"rates_cny": map[string]string{"off_hit": "1", "off_miss": "3", "off_output": output, "peak_hit": "1", "peak_miss": "3", "peak_output": output},
				})
				req := httptest.NewRequest("POST", "/admin/billing/prices", bytes.NewReader(raw))
				req = req.WithContext(context.WithValue(ctx, sessionKey{}, session{Username: "admin"}))
				if err := app.savePrice(httptest.NewRecorder(), req); err != nil {
					t.Fatal(err)
				}
			}
			save(0, "4")
			price, err := store.Price(ctx, channel.Model, time.Now())
			if err != nil || price == nil {
				t.Fatal("saved price missing", err)
			}
			result, err := runTestAudit(t, app, p, key.ID, "production", "priced request")
			// 8 cached * 1 + 2 uncached * 3 + 5 output * 4, per million tokens.
			if err != nil || result.Cost.AmountCNY == nil || *result.Cost.AmountCNY != "0.000034" || result.Cost.Status != "calculated" || result.ActualModel == channel.Model {
				t.Fatal("configured name did not determine price", result, err)
			}
			save(price.ID, "8")
			result, err = runTestAudit(t, app, p, key.ID, "production", "updated price")
			if err != nil || result.Cost.AmountCNY == nil || *result.Cost.AmountCNY != "0.000054" {
				t.Fatal("new request did not use updated price", result, err)
			}
			w := httptest.NewRecorder()
			if err = app.costs(w, httptest.NewRequest("GET", "/admin/billing/costs", nil)); err != nil {
				t.Fatal(err)
			}
			var ledger struct {
				Items   []CostRow `json:"items"`
				Summary struct {
					Calculated string `json:"calculated_cny"`
					Pending    int    `json:"pending_count"`
				} `json:"summary"`
			}
			if err = json.Unmarshal(w.Body.Bytes(), &ledger); err != nil || ledger.Summary.Calculated != "0.000088" || ledger.Summary.Pending != 1 {
				t.Fatal("incorrect cost summary", ledger, err)
			}
			foundSnapshot := false
			for _, row := range ledger.Items {
				if row.Cost.PriceID != nil && *row.Cost.PriceID == price.ID {
					foundSnapshot = true
					if row.Price == nil || row.Price.Rates.OffOutput != 4_000_000 || row.Cost.AmountCNY == nil || *row.Cost.AmountCNY != "0.000034" || row.Cost.Period != pricePeriod(row.StartedAt) {
						t.Fatal("original price snapshot or tariff changed", row)
					}
				}
			}
			if !foundSnapshot {
				t.Fatal("original price snapshot missing")
			}
			budgets, err := store.Budgets(ctx, time.Now())
			if err != nil || len(budgets) != 1 || budgets[0].MonthlyUsed != "0.000088" || budgets[0].MonthlyReserved != "0" {
				t.Fatal("priced calls did not settle budget", budgets, err)
			}
			if _, err = store.DB.Exec("UPDATE client_budgets SET monthly_limit=88000000 WHERE client_id=$1", key.ID); err != nil {
				t.Fatal(err)
			}
			if _, err = runTestAudit(t, app, p, key.ID, "production", "over budget"); errorCode(err) != "budget_exceeded" || calls.Load() != 3 {
				t.Fatal("exhausted budget allowed an upstream call", err, calls.Load())
			}
		})
	}
}

func TestExactResultCacheSavesTokensAndHonorsScopeAndBudget(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	p, key := configureBillingPolicy(t, store, 60)
	cfg := testInference(t, store, p)
	app, err := NewServer(store, "http://localhost:8090", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	app.Engine.Client = &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		body := `{"choices":[{"finish_reason":"stop","message":{"content":"{\"confidence\":0.1,\"reason\":\"正常开发\"}"}}],"usage":{"prompt_tokens":100,"completion_tokens":10,"total_tokens":110,"prompt_cache_hit_tokens":80,"prompt_cache_miss_tokens":20}}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewBufferString(body))}, nil
	})}
	first, err := runTestAudit(t, app, p, key.ID, "production", "same exact content")
	if err != nil || first.CacheHit || first.Cost.Status != "calculated" {
		t.Fatal("first audit failed", first, err)
	}
	if _, err = store.DB.Exec("INSERT INTO client_budgets(client_id,daily_limit,monthly_limit) VALUES($1,0,0)", key.ID); err != nil {
		t.Fatal(err)
	}
	second, err := runTestAudit(t, app, p, key.ID, "production", "same exact content")
	if err != nil || !second.CacheHit || second.Usage.TotalTokens != 0 || second.Cost.AmountCNY == nil || *second.Cost.AmountCNY != "0" || calls.Load() != 1 {
		t.Fatal("cache incurred a model call", second, err)
	}
	if _, err = runTestAudit(t, app, p, key.ID, "production", "different content"); err == nil || calls.Load() != 1 {
		t.Fatal("budget did not prevent new model call")
	}
	if _, err = runTestAudit(t, app, changedPolicy(p), key.ID, "production", "same exact content"); err == nil || calls.Load() != 1 {
		t.Fatal("old configuration cache reused")
	}
	secret, _ := store.CredentialSecret(ctx, cfg.CredentialID)
	a := store.assessmentCacheKey(key.ID, p, cfg, 1, secret, "text")
	if a == store.assessmentCacheKey("other", p, cfg, 1, secret, "text") || a == store.assessmentCacheKey(key.ID, p, cfg, 1, "rotated-key", "text") {
		t.Fatal("cache scope collision")
	}
	trial, err := runTestAudit(t, app, p, "admin", "test", "same exact content")
	if err != nil || trial.CacheHit || calls.Load() != 2 {
		t.Fatal("trial should call model and be separate from caller budget", err)
	}
	req := httptest.NewRequest("GET", "/admin/billing/costs", nil)
	w := httptest.NewRecorder()
	if err = app.costs(w, req); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(w.Body.Bytes(), []byte(`"local_cache_hits":1`)) {
		t.Fatal("cache not visible in cost report", w.Body.String())
	}
}

func TestBillingAdminAPIAuthorizationAndVersionedUpdates(t *testing.T) {
	store := testStore(t)
	_, key := configureBillingPolicy(t, store, 0)
	app, err := NewServer(store, "http://localhost:8090", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.DB.Exec("INSERT INTO admin_sessions(token_hash,username,csrf,expires_at) VALUES($1,'admin','test-csrf',NOW()+INTERVAL '1 hour')", digest("test-admin-session"))
	if err != nil {
		t.Fatal(err)
	}
	handler := app.Handler()
	call := func(method, path string, body any, authenticated bool, want int) []byte {
		t.Helper()
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(method, path, bytes.NewReader(raw))
		req.Header.Set("Origin", app.Origin)
		if authenticated {
			req.Header.Set("Cookie", "audit_session=test-admin-session")
			req.Header.Set("X-CSRF-Token", "test-csrf")
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("%s %s got %d want %d: %s", method, path, w.Code, want, w.Body.String())
		}
		return w.Body.Bytes()
	}
	budgetPath := "/admin/api-keys/" + key.ID + "/budget"
	body := map[string]any{"daily_limit_cny": "1.00", "monthly_limit_cny": "20", "expected_revision": 0}
	call("PUT", budgetPath, body, false, 401)
	call("PUT", budgetPath, body, true, 200)
	call("PUT", budgetPath, body, true, 409)
	var budgets []BudgetView
	if err = json.Unmarshal(call("GET", "/admin/billing/budgets", nil, true, 200), &budgets); err != nil {
		t.Fatal(err)
	}
	if len(budgets) != 1 || budgets[0].DailyLimit == nil || *budgets[0].DailyLimit != "1" || *budgets[0].MonthlyLimit != "20" {
		t.Fatal("budget amount contract failed", budgets)
	}
	var prices []PriceCard
	if err = json.Unmarshal(call("GET", "/admin/billing/prices", nil, true, 200), &prices); err != nil {
		t.Fatal(err)
	}
	var flashID int64
	for _, p := range prices {
		if p.Model == "deepseek-flash" {
			flashID = p.ID
		}
	}
	price := map[string]any{"model": "deepseek-flash", "source": "test price publication", "expected_price_id": flashID, "rates_cny": map[string]string{"off_hit": "0.03", "off_miss": "1", "off_output": "4", "peak_hit": "0.06", "peak_miss": "2", "peak_output": "8"}}
	call("POST", "/admin/billing/prices", price, true, 200)
	call("POST", "/admin/billing/prices", price, true, 409)
	current, err := store.Price(context.Background(), "deepseek-flash", time.Now())
	if err != nil || current.ID == flashID || current.Rates.OffHit != 30000 {
		t.Fatal("price publication failed", current, err)
	}
	call("GET", "/admin/billing/costs", nil, false, 401)
}
