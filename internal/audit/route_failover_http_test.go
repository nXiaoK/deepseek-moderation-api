package audit

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

// Model the upstream sending its HTTP status immediately while the error body
// stalls until our request deadline. The received status still owns the cooldown.
type failoverCanceledErrorBody struct{ ctx context.Context }

func (b failoverCanceledErrorBody) Read([]byte) (int, error) {
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}
func (failoverCanceledErrorBody) Close() error { return nil }

func TestRouteFailoverPreservesRetryAfterWhenErrorBodyTimesOut(t *testing.T) {
	store, app, p, key, primaries, backup := failoverFixture(t, 1)
	ctx := context.Background()
	p.Config.PriorityTimeoutMS = 500
	// The first failure must cool down because of Retry-After, rather than the
	// policy's consecutive-failure threshold.
	p.Config.FailureThreshold = 3
	if err := store.SaveConfig(ctx, "admin", p.ID, p.Name, p.Revision, p.Config); err != nil {
		t.Fatal(err)
	}
	var err error
	p, err = store.Policy(ctx, p.ID)
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
			return &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Header:     http.Header{"Retry-After": []string{"3600"}},
				Body:       failoverCanceledErrorBody{ctx: r.Context()},
			}, nil
		}
		return failoverSuccess(), nil
	})}
	res, err := runTestAudit(t, app, p, key.ID, "production", "slow rate-limit error body")
	if err != nil || res.ChannelID != backup.ID || res.AttemptCount != 2 || !slices.Equal(calls, []string{primaries[0].Model, backup.Model}) {
		t.Fatal("rate-limited primary did not hand over to backup", res, calls, err)
	}
	log, err := store.LogDetail(ctx, res.ID)
	if err != nil || len(log.Attempts) != 2 {
		t.Fatal("failover attempts were not retained", log, err)
	}
	failed := log.Attempts[0]
	if !failed.Sent || failed.ErrorCode != "upstream_rate_limited" || !strings.Contains(failed.ErrorMessage, "HTTP 429") {
		t.Fatal("scheduler deadline overwrote the received HTTP error", failed)
	}
	health := app.Engine.channelHealth(primaries[0].ID)
	if health.Status != "cooling" || health.LastErrorCode != "upstream_rate_limited" || health.Calls != 1 || health.Failures != 1 || time.Until(health.CooldownUntil) < 59*time.Minute {
		t.Fatal("scheduler deadline discarded Retry-After cooldown", health)
	}
	if health.InFlight != 0 || app.Engine.channelHealth(backup.ID).InFlight != 0 || len(app.Engine.slots) != 0 {
		t.Fatal("rate-limit failover leaked channel or engine capacity")
	}
}
