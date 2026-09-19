package audit

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func acquireForTest(t *testing.T, e *Engine, cs []routeCandidate, cfg PolicySettings, now time.Time, want string) func(error, bool) {
	t.Helper()
	c, release, err := e.acquire(cs, cfg, nil, now, func(int) int { return 0 })
	if err != nil || c == nil || c.channel.ID != want {
		t.Fatalf("wanted channel %s, got %+v, %v", want, c, err)
	}
	return release
}

func TestConfiguredChannelCooldownAndRecovery(t *testing.T) {
	for _, failure := range []error{
		problem(503, "upstream_unavailable", "failed"),
		problem(504, "upstream_timeout", "timeout"),
		problem(502, "invalid_model_response", "invalid"),
		classifyUpstream(&http.Response{StatusCode: 401}, ""),
		classifyUpstream(&http.Response{StatusCode: 400}, ""),
	} {
		t.Run(errorCode(failure), func(t *testing.T) {
			e := NewEngine(8)
			cs := []routeCandidate{candidate("primary", 1, 1), candidate("backup", 2, 1)}
			cs[0].channel.MaxConcurrency = 8 // Probe exclusion must not rely on capacity.
			cfg := DefaultSettings()
			cfg.FailureThreshold, cfg.FailureCooldownMinutes = 2, 7
			for _, result := range []error{failure, nil, failure, failure} {
				acquireForTest(t, e, cs, cfg, time.Now(), "primary")(result, true)
			}
			until := e.channelHealth("primary").CooldownUntil
			if remaining := time.Until(until); remaining < 7*time.Minute-time.Second || remaining > 7*time.Minute {
				t.Fatalf("wrong configured cooldown: %s", remaining)
			}
			acquireForTest(t, e, cs, cfg, until.Add(-time.Nanosecond), "backup")(nil, true)
			probe := acquireForTest(t, e, cs, cfg, until, "primary")
			acquireForTest(t, e, cs, cfg, until, "backup")(nil, true)
			probe(failure, true)
			if e.channelHealth("primary").Status != "cooling" {
				t.Fatal("failed probe did not re-enter cooldown")
			}
			until = e.channelHealth("primary").CooldownUntil
			acquireForTest(t, e, cs, cfg, until, "primary")(nil, true)
			if e.channelHealth("primary").Status != "ready" || e.routes["primary"].consecutive != 0 {
				t.Fatal("successful probe did not reset failures")
			}
		})
	}
}

func TestSingleChannelAlwaysRetriesAndRetainsCapacityLimit(t *testing.T) {
	for _, failure := range []error{
		problem(503, "upstream_unavailable", "failed"),
		classifyUpstream(&http.Response{StatusCode: 403}, ""),
		classifyUpstream(&http.Response{StatusCode: 429, Header: http.Header{"Retry-After": {"3600"}}}, ""),
	} {
		t.Run(errorCode(failure), func(t *testing.T) {
			e := NewEngine(8)
			cfg := DefaultSettings()
			cs := []routeCandidate{candidate("only", 1, 1)}
			for i := 0; i < 6; i++ {
				release := acquireForTest(t, e, cs, cfg, time.Now(), "only")
				if c, _, err := e.acquire(cs, cfg, nil, time.Now(), func(int) int { return 0 }); c != nil || err != nil {
					t.Fatal("single-channel exception bypassed capacity")
				}
				release(failure, true)
			}
			if !e.channelHealth("only").CooldownUntil.IsZero() {
				t.Fatal("single-channel failures created cooldown")
			}
			// A cooldown created by another policy or before removing a backup
			// must not block the now-sole candidate, or be extended by its failures.
			until := time.Now().Add(time.Hour)
			e.routes["only"].until = until
			acquireForTest(t, e, cs, cfg, time.Now(), "only")(failure, true)
			if e.channelHealth("only").CooldownUntil != until {
				t.Fatal("single-channel request extended a shared cooldown")
			}
			acquireForTest(t, e, cs, cfg, time.Now(), "only")(nil, true)
		})
	}
}

func TestCooldownDoesNotTreatLastUnusedCandidateAsSingleChannel(t *testing.T) {
	e := NewEngine(8)
	cfg := DefaultSettings()
	cfg.FailureThreshold = 1
	cs := []routeCandidate{candidate("primary", 1, 1), candidate("backup", 2, 1)}
	failure := problem(503, "upstream_unavailable", "failed")
	acquireForTest(t, e, cs, cfg, time.Now(), "primary")(failure, true)
	acquireForTest(t, e, cs, cfg, time.Now(), "backup")(failure, true)
	for _, used := range []map[string]bool{nil, {"primary": true}} {
		if c, _, err := e.acquire(cs, cfg, used, time.Now(), func(int) int { return 0 }); c != nil || err != nil {
			t.Fatal("all-cooling route bypassed cooldown", c, err)
		}
	}
}

func TestCooldownIgnoresUnsentAndCancelledCalls(t *testing.T) {
	e := NewEngine(8)
	cfg := DefaultSettings()
	cfg.FailureThreshold = 1
	cs := []routeCandidate{candidate("primary", 1, 1), candidate("backup", 2, 1)}
	acquireForTest(t, e, cs, cfg, time.Now(), "primary")(problem(503, "upstream_unavailable", "failed"), false)
	for _, err := range []error{context.Canceled, context.DeadlineExceeded} {
		acquireForTest(t, e, cs, cfg, time.Now(), "primary")(err, true)
	}
	if e.routes["primary"].consecutive != 0 || e.channelHealth("primary").Status != "ready" {
		t.Fatal("unsent or cancelled call caused cooldown")
	}
}

func TestRateLimitAndConfiguredCooldownUseLongerDelay(t *testing.T) {
	for _, tc := range []struct {
		retryAfter string
		want       time.Duration
	}{
		{"10", 30 * time.Minute}, {"3600", time.Hour},
	} {
		e := NewEngine(8)
		cfg := DefaultSettings()
		cfg.FailureThreshold = 1
		cs := []routeCandidate{candidate("primary", 1, 1), candidate("backup", 2, 1)}
		failure := classifyUpstream(&http.Response{StatusCode: 429, Header: http.Header{"Retry-After": {tc.retryAfter}}}, "")
		acquireForTest(t, e, cs, cfg, time.Now(), "primary")(failure, true)
		if remaining := time.Until(e.channelHealth("primary").CooldownUntil); remaining < tc.want-time.Second || remaining > tc.want {
			t.Fatalf("cooldown = %s, want %s", remaining, tc.want)
		}
	}
}

func TestLegacyAndInvalidCooldownSettings(t *testing.T) {
	raw, _ := json.Marshal(DefaultSettings())
	var old map[string]any
	_ = json.Unmarshal(raw, &old)
	delete(old, "failure_threshold")
	delete(old, "failure_cooldown_minutes")
	raw, _ = json.Marshal(old)
	var cfg PolicySettings
	if err := json.Unmarshal(raw, &cfg); err != nil || cfg.Validate() != nil || cfg.failureThreshold() != 3 || cfg.failureCooldown() != 30*time.Minute {
		t.Fatal("old policy did not inherit defaults", cfg, err)
	}
	for _, change := range []func(*PolicySettings){
		func(c *PolicySettings) { c.FailureThreshold = -1 },
		func(c *PolicySettings) { c.FailureThreshold = 101 },
		func(c *PolicySettings) { c.FailureCooldownMinutes = -1 },
		func(c *PolicySettings) { c.FailureCooldownMinutes = 1441 },
	} {
		cfg := DefaultSettings()
		change(&cfg)
		if cfg.Validate() == nil {
			t.Fatal("invalid cooldown accepted", cfg)
		}
	}
}

func TestRoutePersistsCooldownSettingsAndSkipsFailedPrimary(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	p, key := configureBillingPolicy(t, store, 0)
	channels, err := store.Channels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	primary := channels[0]
	backup := createTestChannel(t, store, primary.CredentialID, "backup", "deepseek-v4-pro")
	p.Config.Channels = append(p.Config.Channels, ChannelBinding{backup.ID, 2, 100, true})
	p.Config.FailureThreshold, p.Config.FailureCooldownMinutes = 2, 9
	if err := store.SaveConfig(ctx, "admin", p.ID, p.Name, p.Revision, p.Config); err != nil {
		t.Fatal(err)
	}
	p, err = store.Policy(ctx, p.ID)
	if err != nil || p.Config.FailureThreshold != 2 || p.Config.FailureCooldownMinutes != 9 {
		t.Fatal("cooldown settings were not persisted", p, err)
	}
	app, err := NewServer(store, "http://localhost:8090", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	primaryCalls, backupCalls := 0, 0
	app.Engine.Client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Model == primary.Model {
			primaryCalls++
			return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader("down"))}, nil
		}
		backupCalls++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"choices":[{"finish_reason":"stop","message":{"content":"{\"confidence\":0.1,\"reason\":\"正常\"}"}}]}`))}, nil
	})}
	for i, wantAttempts := range []int{2, 2, 1} {
		res, err := runTestAudit(t, app, p, key.ID, "production", "cooldown")
		if err != nil || res.ChannelID != backup.ID || res.AttemptCount != wantAttempts {
			t.Fatalf("request %d: %+v, %v", i, res, err)
		}
	}
	if primaryCalls != 2 || backupCalls != 3 {
		t.Fatal("cooling primary was called", primaryCalls, backupCalls)
	}
	if remaining := time.Until(app.Engine.channelHealth(primary.ID).CooldownUntil); remaining < 9*time.Minute-time.Second || remaining > 9*time.Minute {
		t.Fatal("route did not apply saved cooldown", remaining)
	}
	// Disabling the backup leaves one enabled candidate, despite two bindings.
	p.Config.Channels[1].Enabled = false
	for i := 0; i < 2; i++ {
		res, err := runTestAudit(t, app, p, key.ID, "production", "single")
		if errorCode(err) != "upstream_unavailable" || res.AttemptCount != 1 {
			t.Fatal("sole enabled channel was not attempted", res, err)
		}
	}
	if primaryCalls != 4 || backupCalls != 3 {
		t.Fatal("sole candidate was skipped or retried within a request", primaryCalls, backupCalls)
	}
}
