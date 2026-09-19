package audit

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func rpmCandidate(id string, priority, weight, rpm int) routeCandidate {
	c := candidate(id, priority, weight)
	c.channel.RPM, c.channel.MaxConcurrency = rpm, 256
	return c
}

func TestRPMWeightsUseRemainingAllowanceAndPriority(t *testing.T) {
	for _, tc := range []struct {
		draw int
		want string
	}{
		{0, "a"}, {59, "a"}, {60, "b"}, {239, "b"},
	} {
		e := NewEngine(16)
		cs := []routeCandidate{rpmCandidate("a", 1, 1, 60), rpmCandidate("b", 1, 1, 180), rpmCandidate("backup", 2, 1000, 100000)}
		c, release, err := e.acquire(cs, DefaultSettings(), nil, time.Now(), func(n int) int {
			if n != 240 {
				t.Fatalf("RPM weights = %d, want 240", n)
			}
			return tc.draw
		})
		if err != nil || c == nil || c.channel.ID != tc.want {
			t.Fatal(c, err)
		}
		release(nil, true)
	}
	e := NewEngine(16)
	now := time.Now()
	cs := []routeCandidate{rpmCandidate("a", 1, 2, 3), rpmCandidate("b", 1, 1, 4)}
	acquireForTest(t, e, cs, DefaultSettings(), now, "a")(nil, true)
	// Two remaining calls with weight 2 versus four remaining with weight 1.
	c, release, err := e.acquire(cs, DefaultSettings(), nil, now, func(n int) int {
		if n != 8 {
			t.Fatalf("remaining weighted allowance = %d, want 8", n)
		}
		return 4
	})
	if err != nil || c == nil || c.channel.ID != "b" {
		t.Fatal(c, err)
	}
	release(nil, true)
}

func TestRPMFallbackSlidingWindowAndSingleChannelLimit(t *testing.T) {
	e := NewEngine(16)
	cfg := DefaultSettings()
	now := time.Now()
	cs := []routeCandidate{rpmCandidate("a", 1, 1, 1), rpmCandidate("b", 1, 1, 1), rpmCandidate("backup", 2, 1, 1)}
	for _, id := range []string{"a", "b", "backup"} {
		acquireForTest(t, e, cs, cfg, now, id)(nil, true)
	}
	for _, candidates := range [][]routeCandidate{cs, cs[:1]} {
		c, _, err := e.acquire(candidates, cfg, nil, now.Add(time.Minute-time.Nanosecond), func(int) int { return 0 })
		if c != nil || errorCode(err) != "channel_rpm_exceeded" {
			t.Fatal("RPM limit bypassed", c, err)
		}
	}
	acquireForTest(t, e, cs, cfg, now.Add(time.Minute), "a")(nil, true)
	if used := e.routes["a"].rpmUsed(now.Add(time.Minute)); used != 1 {
		t.Fatal("window did not roll", used)
	}
}

func TestRPMReservationsRefundsAndOutOfOrderCompletions(t *testing.T) {
	e := NewEngine(16)
	cfg := DefaultSettings()
	now := time.Now()
	cs := []routeCandidate{rpmCandidate("a", 1, 1, 2)}
	first := acquireForTest(t, e, cs, cfg, now, "a")
	second := acquireForTest(t, e, cs, cfg, now.Add(time.Second), "a")
	if c, _, err := e.acquire(cs, cfg, nil, now.Add(2*time.Minute), func(int) int { return 0 }); c != nil || errorCode(err) != "channel_rpm_exceeded" {
		t.Fatal("outstanding reservations expired", c, err)
	}
	second(nil, true)
	first(context.Canceled, true)
	if used := e.routes["a"].rpmUsed(now.Add(time.Minute)); used != 1 {
		t.Fatal("out-of-order completion changed window", used)
	}
	refund := acquireForTest(t, e, cs, cfg, now.Add(time.Minute), "a")
	refund(nil, false) // Cache hit or failure before sending must return its quota.
	if used := e.routes["a"].rpmUsed(now.Add(time.Minute)); used != 1 {
		t.Fatal("unsent reservation was not refunded", used)
	}
	acquireForTest(t, e, cs, cfg, now.Add(time.Minute), "a")(nil, true)
}

func TestRPMQuotaSurvivesEditsAndReset(t *testing.T) {
	e := NewEngine(16)
	cfg := DefaultSettings()
	now := time.Now()
	cs := []routeCandidate{rpmCandidate("a", 1, 1, 1)}
	release := acquireForTest(t, e, cs, cfg, now, "a")
	e.resetChannel("a")
	cs[0].signature, cs[0].channel.Revision = "edited", 2
	if c, _, err := e.acquire(cs, cfg, nil, now, func(int) int { return 0 }); c != nil || errorCode(err) != "channel_rpm_exceeded" {
		t.Fatal("reset lost reservation", c, err)
	}
	release(nil, true)
	if c, _, err := e.acquire(cs, cfg, nil, now, func(int) int { return 0 }); c != nil || errorCode(err) != "channel_rpm_exceeded" {
		t.Fatal("stale completion lost quota", c, err)
	}
	cs[0].channel.RPM = 2
	acquireForTest(t, e, cs, cfg, now, "a")(nil, true)
	cs[0].channel.RPM = 0
	acquireForTest(t, e, cs, cfg, now, "a")(nil, true)
	if used := e.channelHealth("a").RPMUsed; used != 3 {
		t.Fatal("health lost RPM usage", used)
	}
}

func TestRPMUnlimitedChannelWeights(t *testing.T) {
	e := NewEngine(16)
	cs := []routeCandidate{rpmCandidate("limited", 1, 1, 10), rpmCandidate("unlimited", 1, 2, 0)}
	c, release, err := e.acquire(cs, DefaultSettings(), nil, time.Now(), func(n int) int {
		if n != 30 {
			t.Fatal("unlimited channel weight", n)
		}
		return 10
	})
	if err != nil || c == nil || c.channel.ID != "unlimited" {
		t.Fatal(c, err)
	}
	release(nil, true)
	// With no configured limits the legacy weighting is unchanged.
	cs[0].channel.RPM = 0
	c, release, err = e.acquire(cs, DefaultSettings(), nil, time.Now(), func(n int) int {
		if n != 3 {
			t.Fatal("legacy weight changed", n)
		}
		return 0
	})
	if err != nil || c == nil || c.channel.ID != "limited" {
		t.Fatal(c, err)
	}
	release(nil, true)
}

func TestRPMWindowStartsAtInferenceRatherThanReservation(t *testing.T) {
	e := NewEngine(16)
	cs := []routeCandidate{rpmCandidate("a", 1, 1, 1)}
	now := time.Now()
	c, release, err := e.acquire(cs, DefaultSettings(), nil, now, func(int) int { return 0 })
	if err != nil || c == nil {
		t.Fatal(c, err)
	}
	// Credentials and budget checks may delay inference after reserving quota.
	c.attemptAt = now.Add(10 * time.Second)
	release(nil, true)
	if used := e.routes["a"].rpmUsed(now.Add(time.Minute)); used != 1 {
		t.Fatal("quota expired before inference window", used)
	}
	if used := e.routes["a"].rpmUsed(now.Add(70 * time.Second)); used != 0 {
		t.Fatal("inference window did not expire", used)
	}
}

func TestRPMConcurrentRequestsShareAllowance(t *testing.T) {
	e := NewEngine(256)
	cs := []routeCandidate{rpmCandidate("shared", 1, 1, 7)}
	now := time.Now()
	var wg sync.WaitGroup
	releases := make(chan func(error, bool), 100)
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, release, err := e.acquire(cs, DefaultSettings(), nil, now, func(int) int { return 0 })
			if c != nil {
				releases <- release
			} else if errorCode(err) != "channel_rpm_exceeded" {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	close(releases)
	if len(releases) != 7 {
		t.Fatalf("RPM overspent: %d reservations", len(releases))
	}
	for release := range releases {
		release(nil, true)
	}
}

func TestChannelRPMValidation(t *testing.T) {
	for _, rpm := range []int{-1, 100001} {
		_, err := (&Store{}).SaveChannel(context.Background(), "admin", ModelChannel{Name: "invalid", MaxConcurrency: 8, RPM: rpm})
		if errorCode(err) != "invalid_channel" {
			t.Fatal("invalid RPM accepted", rpm, err)
		}
	}
}

func TestChannelRPMAPIPersistenceAndMigration(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	cred, err := store.SaveCredential(ctx, "admin", "", "test", "test-secret", true)
	if err != nil {
		t.Fatal(err)
	}
	c := createTestChannel(t, store, cred, "legacy", "deepseek-flash")
	if c.RPM != 0 {
		t.Fatal("legacy channel should be unlimited")
	}
	// Replay the new migration over the previous schema, then ensure idempotence.
	if _, err := store.DB.Exec("ALTER TABLE audit_model_channels DROP COLUMN rpm; ALTER TABLE audit_costs DROP COLUMN IF EXISTS reservation_valid; DELETE FROM audit_schema_migrations WHERE version IN (4,5)"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		tx, err := store.DB.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := applyMigrations(ctx, tx); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	app, err := NewServer(store, "http://localhost:8090", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, rpm := range []int{120, 30, 0} {
		raw, _ := json.Marshal(map[string]any{"name": c.Name, "model": c.Model, "credential_id": c.CredentialID, "timeout_ms": c.TimeoutMS, "max_tokens": c.MaxTokens, "max_concurrency": c.MaxConcurrency, "enabled": true, "rpm": rpm, "expected_revision": c.Revision})
		r := httptest.NewRequest("PUT", "/admin/model-channels/"+c.ID, strings.NewReader(string(raw)))
		r.SetPathValue("id", c.ID)
		r = r.WithContext(context.WithValue(ctx, sessionKey{}, session{Username: "admin"}))
		w := httptest.NewRecorder()
		if err := app.saveChannel(w, r); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil || c.RPM != rpm {
			t.Fatal("RPM save failed", c, err)
		}
		channels, err := store.Channels(ctx)
		if err != nil || len(channels) != 1 || channels[0].RPM != rpm {
			t.Fatal("RPM read failed", channels, err)
		}
	}
}

func TestRouteRPMFallbackAndCacheBypass(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	p, key := configureBillingPolicy(t, store, 60)
	channels, err := store.Channels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	primary := channels[0]
	primary.RPM = 1
	primary, err = store.SaveChannel(ctx, "admin", primary)
	if err != nil {
		t.Fatal(err)
	}
	backup := primary
	backup.ID, backup.Name, backup.Model = "", "RPM backup", "deepseek-v4-pro"
	backup, err = store.SaveChannel(ctx, "admin", backup)
	if err != nil || backup.RPM != 1 {
		t.Fatal("create lost RPM", backup, err)
	}
	p.Config.Channels = append(p.Config.Channels, ChannelBinding{backup.ID, 2, 100, true})
	if err := store.SaveConfig(ctx, "admin", p.ID, p.Name, p.Revision, p.Config); err != nil {
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
	calls := 0
	app.Engine.Client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"choices":[{"finish_reason":"stop","message":{"content":"{\"confidence\":0.1,\"reason\":\"正常\"}"}}]}`))}, nil
	})}
	first, err := runTestAudit(t, app, p, key.ID, "production", "first")
	if err != nil || first.ChannelID != primary.ID || first.AttemptCount != 1 {
		t.Fatal(first, err)
	}
	cached, err := runTestAudit(t, app, p, key.ID, "production", "first")
	if err != nil || !cached.CacheHit || cached.AttemptCount != 0 || calls != 1 {
		t.Fatal("RPM blocked cache", cached, err)
	}
	second, err := runTestAudit(t, app, p, key.ID, "production", "second")
	if err != nil || second.ChannelID != backup.ID || second.AttemptCount != 1 || calls != 2 {
		t.Fatal("RPM fallback failed", second, err)
	}
	_, err = runTestAudit(t, app, p, key.ID, "production", "third")
	if errorCode(err) != "channel_rpm_exceeded" || calls != 2 {
		t.Fatal("RPM limit not enforced", calls, err)
	}
	// Trials share the same allowance, and do not use the production cache.
	_, err = runTestAudit(t, app, p, key.ID, "test", "first")
	if errorCode(err) != "channel_rpm_exceeded" || calls != 2 {
		t.Fatal("trial bypassed shared RPM", calls, err)
	}
}
