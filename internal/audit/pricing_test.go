package audit

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func intPtr(n int) *int { return &n }
func flashCard() PriceCard {
	return PriceCard{ID: 1, Model: "deepseek-flash", Rates: PriceRates{20000, 1000000, 4000000, 40000, 2000000, 8000000}}
}
func parseTime(t *testing.T, value string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return at
}
func TestDeepSeekTariffBoundaries(t *testing.T) {
	cases := map[string]string{"2026-09-14T08:59:59+08:00": "off_peak", "2026-09-14T09:00:00+08:00": "peak", "2026-09-14T12:00:00+08:00": "off_peak", "2026-09-14T14:00:00+08:00": "peak", "2026-09-18T17:59:59+08:00": "peak", "2026-09-18T18:00:00+08:00": "off_peak", "2026-09-19T10:00:00+08:00": "off_peak", "2026-09-14T01:00:00Z": "peak"}
	for input, want := range cases {
		if got := pricePeriod(parseTime(t, input)); got != want {
			t.Fatalf("%s got %s want %s", input, got, want)
		}
	}
}

func TestReserveCoversEveryValidInputTariff(t *testing.T) {
	cfg := PolicyConfig{Prompt: "system", MaxTokens: 32}
	input := "test input"
	inputTokens := len(cfg.Prompt) + len(input) + 1024
	off := parseTime(t, "2026-09-12T10:00:00+08:00")
	peak := parseTime(t, "2026-09-14T10:00:00+08:00")
	for _, tc := range []struct {
		name  string
		rates PriceRates
	}{
		{"off_peak_hit", PriceRates{OffHit: 1_000_000_000}},
		{"peak_hit", PriceRates{PeakHit: 1_000_000_000}},
		{"off_peak_miss", PriceRates{OffMiss: 1_000_000_000}},
		{"peak_miss", PriceRates{PeakMiss: 1_000_000_000}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			card := PriceCard{Rates: tc.rates}
			if err := card.Rates.Validate(); err != nil {
				t.Fatal(err)
			}
			reserved, err := card.reserve(cfg, input)
			if err != nil || reserved != int64(inputTokens)*1_000_000_000 {
				t.Fatalf("highest input tariff not reserved: %d, %v", reserved, err)
			}
			for _, at := range []time.Time{off, peak} {
				for _, hits := range []int{0, inputTokens / 2, inputTokens} {
					u := Usage{Reported: true, PromptTokens: inputTokens, CacheHitTokens: intPtr(hits), CacheMissTokens: intPtr(inputTokens - hits)}
					actual, _, _, err := card.calculate(u, at, at)
					if err != nil || actual > reserved {
						t.Fatalf("actual cost %d exceeds reserve %d: %v", actual, reserved, err)
					}
				}
			}
		})
	}
}

func TestCachedInputPriceCannotBypassZeroBudget(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	p, key := configureBillingPolicy(t, store, 0)
	cfg := testInference(t, store, p)
	raw, _ := json.Marshal(PriceRates{OffHit: 1_000_000_000, PeakHit: 1_000_000_000})
	if _, err := store.DB.Exec("INSERT INTO model_prices(model,rates,source,author) VALUES($1,$2,'regression tariff','admin')", cfg.Model, string(raw)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.Exec("INSERT INTO client_budgets(client_id,daily_limit,monthly_limit) VALUES($1,0,0)", key.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReserveCost(ctx, randomToken("cost_"), key.ID, "production", p, cfg, "priced cache hits", false, time.Now()); errorCode(err) != "budget_exceeded" {
		t.Fatal("nonzero cached-token tariff bypassed zero budget", err)
	}
}

func TestExactCacheAwareCostAndMissingUsage(t *testing.T) {
	c := flashCard()
	start := parseTime(t, "2026-09-12T10:00:00+08:00")
	u := Usage{PromptTokens: 7000, CompletionTokens: 40, TotalTokens: 7040, CacheHitTokens: intPtr(6000), CacheMissTokens: intPtr(1000), Reported: true}
	amount, status, _, err := c.calculate(u, start, start.Add(time.Second))
	if err != nil || status != "calculated" || picoString(amount) != "0.00128" {
		t.Fatalf("cost=%s status=%s err=%v", picoString(amount), status, err)
	}
	peak := parseTime(t, "2026-09-14T10:00:00+08:00")
	amount, status, _, err = c.calculate(u, peak, peak.Add(time.Second))
	if err != nil || picoString(amount) != "0.00256" {
		t.Fatal("wrong peak price", amount, err)
	}
	boundary := parseTime(t, "2026-09-14T08:59:59+08:00")
	amount, status, _, err = c.calculate(u, boundary, boundary.Add(2*time.Second))
	if err != nil || status != "estimated" || picoString(amount) != "0.00256" {
		t.Fatal("boundary did not use high estimate")
	}
	u.CacheHitTokens = nil
	u.CacheMissTokens = nil
	amount, status, _, err = c.calculate(u, start, start)
	if err != nil || status != "estimated" || picoString(amount) != "0.00716" {
		t.Fatal("missing cache split must be estimated", picoString(amount), status)
	}
	u.Reported = false
	_, status, _, err = c.calculate(u, start, start)
	if err != nil || status != "pending" {
		t.Fatal("missing usage treated as zero")
	}
	for _, value := range []string{"0", "0.02", "0.000000000001", "12.345678901234", "100000"} {
		n, err := parseCNY(value)
		if err != nil || picoString(n) != value {
			t.Fatalf("money roundtrip %s: %d %v", value, n, err)
		}
	}
	for _, value := range []string{"-1", "NaN", "1e3", "1.1234567890123", "1000000", "1;drop"} {
		if _, err := parseCNY(value); err == nil {
			t.Fatal("invalid money accepted", value)
		}
	}
}
func TestUsageMetadataValidityAndPersistence(t *testing.T) {
	for _, raw := range []string{`{}`, `null`, `{"prompt_tokens":null,"completion_tokens":1,"total_tokens":1}`, `{"prompt_tokens":-1,"completion_tokens":1,"total_tokens":0}`, `{"prompt_tokens":10,"completion_tokens":1,"total_tokens":10}`} {
		var u Usage
		if err := json.Unmarshal([]byte(raw), &u); err != nil {
			t.Fatal(err)
		}
		if u.Reported {
			t.Fatal("invalid usage accepted", raw)
		}
	}
	var u Usage
	raw := `{"prompt_tokens":100,"completion_tokens":10,"total_tokens":110,"prompt_cache_hit_tokens":80,"prompt_cache_miss_tokens":20,"completion_tokens_details":{"reasoning_tokens":4}}`
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatal(err)
	}
	if !u.Reported || *u.CacheHitTokens != 80 || u.ReasoningTokens != 4 {
		t.Fatal("usage fields lost")
	}
	saved, _ := json.Marshal(u)
	var restored Usage
	_ = json.Unmarshal(saved, &restored)
	if !restored.Reported || restored.ReasoningTokens != 4 {
		t.Fatal("persisted usage changed")
	}
	_ = json.Unmarshal([]byte(strings.Replace(raw, `"prompt_cache_miss_tokens":20`, `"prompt_cache_miss_tokens":21`, 1)), &u)
	if !u.Reported || u.CacheHitTokens != nil {
		t.Fatal("inconsistent cache split used for billing")
	}
	saved, _ = json.Marshal(Usage{})
	_ = json.Unmarshal(saved, &restored)
	if restored.Reported {
		t.Fatal("unknown persisted usage became reported zero")
	}
}
