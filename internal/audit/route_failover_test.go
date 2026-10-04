package audit

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestRouteFailoverSettingsDefaultsAndValidation(t *testing.T) {
	cfg := DefaultSettings()
	cfg.MaxAttemptsPerPriority, cfg.PriorityTimeoutMS = 0, 0
	if cfg.maxAttemptsPerPriority() != 2 || cfg.priorityTimeout() != 4*time.Second || cfg.Validate() != nil {
		t.Fatal("legacy policies did not inherit failover defaults", cfg)
	}
	for _, change := range []func(*PolicySettings){
		func(c *PolicySettings) { c.MaxAttemptsPerPriority = -1 },
		func(c *PolicySettings) { c.MaxAttemptsPerPriority = 6 },
		func(c *PolicySettings) { c.PriorityTimeoutMS = -1 },
		func(c *PolicySettings) { c.PriorityTimeoutMS = 99 },
		func(c *PolicySettings) { c.PriorityTimeoutMS = 25001 },
	} {
		cfg = DefaultSettings()
		change(&cfg)
		if cfg.Validate() == nil {
			t.Fatal("invalid failover settings accepted", cfg)
		}
	}
	for _, value := range []int{1, 5} {
		cfg = DefaultSettings()
		cfg.MaxAttemptsPerPriority = value
		cfg.PriorityTimeoutMS = 100
		if cfg.Validate() != nil || cfg.maxAttemptsPerPriority() != value || cfg.priorityTimeout() != 100*time.Millisecond {
			t.Fatal("valid failover settings rejected", cfg)
		}
	}
}

func TestRouteProgressAdvancesExhaustedPriorityAndReservesLastAttempt(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	cs := []routeCandidate{candidate("a", 1, 1), candidate("b", 1, 1), candidate("c", 1, 1), candidate("backup", 2, 1)}
	for _, tc := range []struct {
		name     string
		perTier  int
		attempts int
		limit    int
	}{
		{"priority attempt cap", 1, 1, 3},
		{"reserve final total attempt", 5, 2, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultSettings()
			cfg.MaxAttemptsPerPriority = tc.perTier
			progress := newRouteProgress(context.Background(), tc.limit)
			progress.attempts[1], progress.totalAttempts = tc.attempts, tc.attempts
			got, _ := progress.selectTier(cs, []int{0, 1, 2, 3}, cfg, now)
			if !slices.Equal(got, []int{3}) {
				t.Fatal("higher priority consumed backup opportunity", got)
			}
		})
	}
	// A tier cap is useful only when another priority can take over.
	cfg := DefaultSettings()
	cfg.MaxAttemptsPerPriority = 1
	progress := newRouteProgress(context.Background(), 3)
	progress.attempts[1], progress.totalAttempts = 1, 1
	got, _ := progress.selectTier(cs, []int{1, 2}, cfg, now)
	if !slices.Equal(got, []int{1, 2}) {
		t.Fatal("tier cap discarded all remaining candidates without backup", got)
	}
}

func TestRouteProgressSharesPriorityDeadlineAndKeepsTimeForBackup(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	ctx, cancel := context.WithDeadline(context.Background(), now.Add(10*time.Second))
	defer cancel()
	cs := []routeCandidate{candidate("a", 1, 1), candidate("b", 1, 1), candidate("backup", 2, 1)}
	cfg := DefaultSettings()
	cfg.MaxAttemptsPerPriority = 5
	cfg.PriorityTimeoutMS = 4000
	progress := newRouteProgress(ctx, 5)
	got, first := progress.selectTier(cs, []int{0, 1, 2}, cfg, now)
	if !slices.Equal(got, []int{0, 1}) || !first.Equal(now.Add(4*time.Second)) {
		t.Fatal("primary did not get configured tier budget", got, first)
	}
	progress.attempts[1], progress.totalAttempts = 1, 1
	got, second := progress.selectTier(cs, []int{1, 2}, cfg, now.Add(time.Second))
	if !slices.Equal(got, []int{1}) || !second.Equal(first) {
		t.Fatal("next primary restarted the tier time budget", got, first, second)
	}
	got, _ = progress.selectTier(cs, []int{1, 2}, cfg, first)
	if !slices.Equal(got, []int{2}) {
		t.Fatal("expired primary tier did not advance", got)
	}
	// A configured tier budget longer than the total budget must leave time
	// for another candidate, rather than running until the global deadline.
	cfg.PriorityTimeoutMS = 9000
	progress = newRouteProgress(ctx, 3)
	_, deadline := progress.selectTier(cs, []int{0, 1, 2}, cfg, now)
	if !deadline.Equal(now.Add(5 * time.Second)) {
		t.Fatal("backup did not retain half of remaining time", deadline)
	}
	progress = newRouteProgress(ctx, 1)
	got, deadline = progress.selectTier(cs, []int{0, 1, 2}, cfg, now)
	if !slices.Equal(got, []int{0, 1}) || !deadline.IsZero() {
		t.Fatal("single total attempt was shortened for an unreachable backup", got, deadline)
	}
}

func TestRouteFailoverUsesOnlySchedulableBackups(t *testing.T) {
	for _, blocked := range []string{"cooldown", "probe", "concurrency", "rpm"} {
		t.Run(blocked, func(t *testing.T) {
			e := NewEngine(8)
			now := time.Now()
			cs := []routeCandidate{candidate("primary", 1, 1), candidate("backup", 2, 1)}
			cs[1].channel.RPM = 1
			state := &channelState{signature: cs[1].signature}
			switch blocked {
			case "cooldown":
				state.until = now.Add(time.Minute)
			case "probe":
				state.probe = true
			case "concurrency":
				state.inFlight = cs[1].channel.MaxConcurrency
			case "rpm":
				state.rpmCalls = []time.Time{now}
			}
			e.routes["backup"] = state
			cfg := DefaultSettings()
			cfg.MaxAttemptsPerPriority, cfg.PriorityTimeoutMS = 1, 100
			ctx, cancel := context.WithDeadline(context.Background(), now.Add(10*time.Second))
			defer cancel()
			progress := newRouteProgress(ctx, 3)
			progress.attempts[1], progress.totalAttempts = 1, 1
			c, release, err := e.acquireRoute(cs, cfg, nil, now, func(int) int { return 0 }, progress)
			if err != nil || c == nil || c.channel.ID != "primary" {
				t.Fatal("unavailable backup displaced primary", c, err)
			}
			release(nil, false)
			if !progress.deadlines[1].IsZero() || progress.skipped[1] {
				t.Fatal("unavailable backup shortened or exhausted primary budget", progress)
			}
		})
	}
}

func failoverFixture(t *testing.T, primaryCount int) (*Store, *Server, Policy, ClientKey, []ModelChannel, ModelChannel) {
	t.Helper()
	store := testStore(t)
	ctx := context.Background()
	p, key := configureBillingPolicy(t, store, 0)
	channels, err := store.Channels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	primary := channels[0]
	primary.TimeoutMS = 25000
	primary, err = store.SaveChannel(ctx, "admin", primary)
	if err != nil {
		t.Fatal(err)
	}
	primaries := []ModelChannel{primary}
	for i := 1; i < primaryCount; i++ {
		c := createTestChannel(t, store, primary.CredentialID, "additional primary", "deepseek-flash")
		primaries = append(primaries, c)
		p.Config.Channels = append(p.Config.Channels, ChannelBinding{c.ID, 1, 100, true})
	}
	backup := createTestChannel(t, store, primary.CredentialID, "fallback backup", "deepseek-v4-pro")
	p.Config.Channels = append(p.Config.Channels, ChannelBinding{backup.ID, 2, 100, true})
	p.Config.MaxAttempts, p.Config.MaxAttemptsPerPriority = 3, 2
	p.Config.TotalTimeoutMS, p.Config.PriorityTimeoutMS = 9000, 100
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
	return store, app, p, key, primaries, backup
}

func failoverSuccess() *http.Response {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"model":"fallback-actual-model","choices":[{"finish_reason":"stop","message":{"content":"{\"confidence\":0.1,\"reason\":\"正常\"}"}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))}
}

func TestRouteFailoverSlowPrimaryKeepsLogsCostsAndCooldown(t *testing.T) {
	store, app, p, key, primaries, backup := failoverFixture(t, 1)
	ctx := context.Background()
	p.Config.FailureThreshold = 1
	if err := store.SaveConfig(ctx, "admin", p.ID, p.Name, p.Revision, p.Config); err != nil {
		t.Fatal(err)
	}
	p, err := store.Policy(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	var calls []string
	app.Engine.Client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		var payload struct{ Model string }
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		calls = append(calls, payload.Model)
		if payload.Model == primaries[0].Model {
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
		return failoverSuccess(), nil
	})}
	res, err := runTestAudit(t, app, p, key.ID, "production", "slow primary")
	if err != nil || res.ChannelID != backup.ID || res.AttemptCount != 2 || !slices.Equal(calls, []string{primaries[0].Model, backup.Model}) {
		t.Fatal("slow primary did not hand over to backup", res, calls, err)
	}
	log, err := store.LogDetail(ctx, res.ID)
	if err != nil || len(log.Attempts) != 2 {
		t.Fatal("failover attempts were not retained", log, err)
	}
	failed := log.Attempts[0]
	if !failed.Sent || failed.ErrorCode != "upstream_timeout" || !strings.Contains(failed.ErrorMessage, "调度时间预算") || failed.Cost == nil || failed.Cost.Status != "pending" || failed.Cost.AmountCNY != nil {
		t.Fatal("scheduler timeout lost failed sent request accounting", failed)
	}
	if res.Cost == nil || res.Cost.Status != "pending" || res.Cost.AmountCNY != nil || res.Usage.TotalTokens != 15 || res.Usage.Reported {
		t.Fatal("failed and successful usage/cost were not aggregated", res)
	}
	health := app.Engine.channelHealth(primaries[0].ID)
	if health.Status != "cooling" || health.LastErrorCode != "upstream_timeout" || health.Calls != 1 || health.Failures != 1 || health.InFlight != 0 {
		t.Fatal("scheduler timeout did not participate in normal channel cooldown", health)
	}
	if app.Engine.channelHealth(backup.ID).InFlight != 0 || len(app.Engine.slots) != 0 {
		t.Fatal("failover leaked channel or engine capacity")
	}
	var costs int
	if err := store.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM audit_costs WHERE request_id=$1", res.ID).Scan(&costs); err != nil || costs != 2 {
		t.Fatal("a failover attempt cost record is missing", costs, err)
	}
}

func TestRouteFailoverManyPrimariesLeaveAttemptForBackup(t *testing.T) {
	store, app, p, key, primaries, backup := failoverFixture(t, 4)
	ctx := context.Background()
	p.Config.MaxAttemptsPerPriority = 5
	p.Config.PriorityTimeoutMS = 4000 // Isolate attempt allocation from database latency.
	if err := store.SaveConfig(ctx, "admin", p.ID, p.Name, p.Revision, p.Config); err != nil {
		t.Fatal(err)
	}
	p, err := store.Policy(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	primaryCalls, backupCalls := 0, 0
	app.Engine.Client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		var payload struct{ Model string }
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.Model == primaries[0].Model {
			primaryCalls++
			return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("unavailable"))}, nil
		}
		backupCalls++
		return failoverSuccess(), nil
	})}
	res, err := runTestAudit(t, app, p, key.ID, "production", "reserve backup attempt")
	if err != nil || res.ChannelID != backup.ID || res.AttemptCount != 3 || primaryCalls != 2 || backupCalls != 1 {
		t.Fatal("primaries consumed every total attempt", res, primaryCalls, backupCalls, err)
	}
}

func TestRouteFailoverDoesNotShortenSingleOrExplicitChannel(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		name := "sole enabled channel"
		if explicit {
			name = "explicit channel trial"
		}
		t.Run(name, func(t *testing.T) {
			store, app, p, key, primaries, _ := failoverFixture(t, 1)
			ctx := context.Background()
			kind, only := "production", ""
			if explicit {
				kind, only = "test", primaries[0].ID
			} else {
				p.Config.Channels[1].Enabled = false
				if err := store.SaveConfig(ctx, "admin", p.ID, p.Name, p.Revision, p.Config); err != nil {
					t.Fatal(err)
				}
				var err error
				p, err = store.Policy(ctx, p.ID)
				if err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			app.Engine.Client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				deadline, ok := r.Context().Deadline()
				if !ok || time.Until(deadline) < 5*time.Second {
					t.Fatal("unreachable backup shortened channel time", deadline)
				}
				return failoverSuccess(), nil
			})}
			_, channels, err := store.RouteSnapshot(ctx, p.ID, nil)
			if err != nil {
				t.Fatal(err)
			}
			res, err := app.runAudit(ctx, p, channels, key.ID, kind, "single primary", only)
			if err != nil || calls != 1 || res.AttemptCount != 1 || res.ChannelID != primaries[0].ID {
				t.Fatal("single or explicit channel changed route", res, calls, err)
			}
		})
	}
}

func TestRouteFailoverCallerCancellationStopsWithoutCooldown(t *testing.T) {
	store, app, p, key, primaries, _ := failoverFixture(t, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	app.Engine.Client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		cancel()
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	_, channels, err := store.RouteSnapshot(context.Background(), p.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := app.runAudit(ctx, p, channels, key.ID, "production", "caller cancelled", "")
	if errorCode(err) != "audit_timeout" || calls != 1 || res.AttemptCount != 1 {
		t.Fatal("caller cancellation launched fallback", res, calls, err)
	}
	health := app.Engine.channelHealth(primaries[0].ID)
	if health.Status != "ready" || health.LastErrorCode != "request_cancelled" || !health.CooldownUntil.IsZero() || health.InFlight != 0 {
		t.Fatal("caller cancellation triggered channel cooldown", health)
	}
}

func TestRouteFailoverPreparationBudgetBoundsCostReservation(t *testing.T) {
	store, app, p, key, primaries, backup := failoverFixture(t, 1)
	ctx := context.Background()
	tx, err := store.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var locked string
	if err := tx.QueryRowContext(ctx, "SELECT id FROM client_api_keys WHERE id=$1 FOR UPDATE", key.ID).Scan(&locked); err != nil {
		t.Fatal(err)
	}
	calls := 0
	app.Engine.Client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return failoverSuccess(), nil
	})}
	start := time.Now()
	res, err := runTestAudit(t, app, p, key.ID, "production", "locked cost reservation")
	elapsed := time.Since(start)
	if errorCode(err) != "cost_record_unavailable" || calls != 0 || res.AttemptCount != 0 {
		t.Fatal("reservation failure sent a request or triggered fallback", res, calls, err)
	}
	if elapsed >= 3*time.Second {
		t.Fatal("cost reservation consumed the global nine-second budget", elapsed)
	}
	for _, c := range []ModelChannel{primaries[0], backup} {
		health := app.Engine.channelHealth(c.ID)
		if health.Calls != 0 || health.Failures != 0 || health.InFlight != 0 || health.RPMUsed != 0 {
			t.Fatal("unsent preparation failure consumed capacity or channel health", c.ID, health)
		}
	}
	app.Engine.routeMu.Lock()
	pending := 0
	for _, state := range app.Engine.routes {
		pending += state.rpmPending
	}
	app.Engine.routeMu.Unlock()
	if pending != 0 || len(app.Engine.slots) != 0 {
		t.Fatal("preparation timeout leaked RPM or engine slots", pending, len(app.Engine.slots))
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var costs, sent int
	if err := store.DB.QueryRowContext(ctx, "SELECT COUNT(*),COUNT(*) FILTER(WHERE request_sent) FROM audit_costs WHERE request_id=$1", res.ID).Scan(&costs, &sent); err != nil || costs != 0 || sent != 0 {
		t.Fatal("aborted preparation persisted a sent or reserved cost", costs, sent, err)
	}
}
