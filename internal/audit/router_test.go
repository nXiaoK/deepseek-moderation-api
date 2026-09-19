package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func createTestChannel(t *testing.T, s *Store, cred, name, model string) ModelChannel {
	t.Helper()
	c, err := s.SaveChannel(context.Background(), "admin", ModelChannel{Name: name, Model: model, CredentialID: cred, TimeoutMS: 4000, MaxTokens: 512, MaxConcurrency: 16, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func testInference(t *testing.T, s *Store, p Policy) PolicyConfig {
	t.Helper()
	_, channels, err := s.RouteSnapshot(context.Background(), p.ID, nil)
	if err != nil || len(channels) == 0 {
		t.Fatal(err)
	}
	return channels[0].Inference(p.Config)
}
func changedPolicy(p Policy) Policy { p.Revision++; return p }
func runTestAudit(t *testing.T, s *Server, p Policy, client, kind, input string) (Response, error) {
	t.Helper()
	_, channels, err := s.Store.RouteSnapshot(context.Background(), p.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s.runAudit(context.Background(), p, channels, client, kind, input, "")
}
func candidate(id string, priority, weight int) routeCandidate {
	return routeCandidate{channel: ModelChannel{ID: id, MaxConcurrency: 1}, binding: ChannelBinding{ChannelID: id, Priority: priority, Weight: weight, Enabled: true}, signature: id}
}
func TestRouterPriorityWeightCapacityAndCooldown(t *testing.T) {
	e := NewEngine(16)
	cs := []routeCandidate{candidate("a", 1, 70), candidate("b", 1, 30), candidate("backup", 2, 100)}
	now := time.Now()
	for _, tt := range []struct {
		draw int
		want string
	}{{0, "a"}, {69, "a"}, {70, "b"}, {99, "b"}} {
		c, release, err := e.acquire(cs, DefaultSettings(), nil, now, func(n int) int {
			if n != 100 {
				t.Fatalf("weights: %d", n)
			}
			return tt.draw
		})
		if err != nil || c.channel.ID != tt.want {
			t.Fatal(c, err)
		}
		release(nil, true)
	}
	a, releaseA, _ := e.acquire(cs, DefaultSettings(), nil, now, func(int) int { return 0 })
	b, releaseB, _ := e.acquire(cs, DefaultSettings(), nil, now, func(int) int { return 0 })
	backup, releaseC, _ := e.acquire(cs, DefaultSettings(), nil, now, func(int) int { return 0 })
	if a.channel.ID != "a" || b.channel.ID != "b" || backup.channel.ID != "backup" {
		t.Fatal("capacity did not advance priority")
	}
	releaseA(nil, true)
	releaseB(nil, true)
	releaseC(nil, true)
	for i := 0; i < 3; i++ {
		_, done, _ := e.acquire(cs, DefaultSettings(), map[string]bool{"b": true, "backup": true}, now, func(int) int { return 0 })
		done(problem(503, "upstream_unavailable", "test"), true)
	}
	c, release, _ := e.acquire(cs, DefaultSettings(), nil, time.Now(), func(int) int { return 0 })
	if c.channel.ID != "b" {
		t.Fatal("cooling model selected")
	}
	release(nil, true)
	c, release, _ = e.acquire(cs, DefaultSettings(), nil, time.Now().Add(31*time.Minute), func(int) int { return 0 })
	if c.channel.ID != "a" {
		t.Fatal("half-open probe missing")
	}
	next, done, _ := e.acquire(cs, DefaultSettings(), nil, time.Now().Add(31*time.Minute), func(int) int { return 0 })
	if next.channel.ID != "b" {
		t.Fatal("multiple half-open probes")
	}
	done(nil, true)
	release(nil, true)
	if e.channelHealth("a").Status != "ready" {
		t.Fatal("probe did not restore")
	}
}
func TestRouterConcurrentReservationAndStaleCompletion(t *testing.T) {
	e := NewEngine(16)
	cs := []routeCandidate{candidate("a", 1, 1)}
	var wg sync.WaitGroup
	acquired := make(chan func(error, bool), 50)
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, release, _ := e.acquire(cs, DefaultSettings(), nil, time.Now(), func(int) int { return 0 })
			if c != nil {
				acquired <- release
			}
		}()
	}
	wg.Wait()
	close(acquired)
	if len(acquired) != 1 {
		t.Fatalf("concurrency exceeded: %d", len(acquired))
	}
	e.resetChannel("a")
	for done := range acquired {
		done(&upstreamFailure{APIError: &APIError{503, "upstream_auth_failed", "bad key"}}, true)
	}
	if e.channelHealth("a").Status != "ready" || e.channelHealth("a").InFlight != 0 {
		t.Fatal("stale call poisoned reset state")
	}
}
func TestRouterHTTPClassification(t *testing.T) {
	for _, code := range []int{401, 403, 400, 404, 429, 500} {
		err := classifyUpstream(&http.Response{StatusCode: code, Header: http.Header{"Retry-After": []string{"123"}}}, "")
		var e *upstreamFailure
		if !errors.As(err, &e) || !retryable(err) {
			t.Fatal(err)
		}
		if code == 429 && e.cooldown != 123*time.Second {
			t.Fatal("retry-after ignored")
		}
		if code < 429 && e.Code != "upstream_auth_failed" && e.Code != "upstream_config_invalid" {
			t.Fatal("configuration error not classified")
		}
	}
	if retryable(problem(429, "budget_exceeded", "")) || retryable(problem(503, "cost_record_unavailable", "")) {
		t.Fatal("non-model error retried")
	}
}
func TestMultiModelFallbackCostsLogsAndValidVerdict(t *testing.T) {
	store := testStore(t)
	p, key := configureBillingPolicy(t, store, 0)
	channels, _ := store.Channels(context.Background())
	first := channels[0]
	second := createTestChannel(t, store, first.CredentialID, "backup", "deepseek-v4-pro")
	p.Config.Channels = append(p.Config.Channels, ChannelBinding{second.ID, 2, 100, true})
	if err := store.SaveConfig(context.Background(), "admin", p.ID, p.Name, p.Revision, p.Config); err != nil {
		t.Fatal(err)
	}
	p, _ = store.Policy(context.Background(), p.ID)
	app, _ := NewServer(store, "http://localhost:8090", t.TempDir())
	calls := 0
	app.Engine.Client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"model":"deepseek-flash"`) {
			return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader("down"))}, nil
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"model":"actual-review-model","choices":[{"finish_reason":"stop","message":{"content":"{\"confidence\":0.9,\"reason\":\"违规\"}"}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))}, nil
	})}
	res, err := runTestAudit(t, app, p, key.ID, "production", "hello")
	if err != nil || calls != 2 || !res.Results[0].Flagged || res.AttemptCount != 2 || res.ActualModel != "actual-review-model" {
		t.Fatalf("fallback failed: %+v %v", res, err)
	}
	if res.Cost.AmountCNY != nil {
		t.Fatal("failed sent request counted free")
	}
	l, err := store.LogDetail(context.Background(), res.ID)
	if err != nil || len(l.Attempts) != 2 || l.Attempts[0].ErrorCode != "upstream_unavailable" || l.Attempts[1].ErrorCode != "" {
		t.Fatal(l, err)
	}
	var count int
	if err = store.DB.QueryRow("SELECT COUNT(*) FROM audit_costs WHERE request_id=$1", res.ID).Scan(&count); err != nil || count != 2 {
		t.Fatal("attempt costs missing", count, err)
	}
	var requests int
	_ = store.DB.QueryRow("SELECT COUNT(*) FROM audit_requests WHERE id=$1", res.ID).Scan(&requests)
	if requests != 1 {
		t.Fatal("duplicated root requests")
	}
	// A valid flagged result must stop routing, rather than seeking an allow.
	app.Engine.Client = &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"choices":[{"finish_reason":"stop","message":{"content":"{\"confidence\":1,\"reason\":\"命中\"}"}}]}`))}, nil
	})}
	before := calls
	res, err = runTestAudit(t, app, p, key.ID, "production", "again")
	if err != nil || calls != before+1 || res.AttemptCount != 1 {
		t.Fatal("valid verdict was retried", err)
	}
}
func TestRouteDeadlineAndAttemptLimit(t *testing.T) {
	store := testStore(t)
	p, key := configureBillingPolicy(t, store, 0)
	channels, _ := store.Channels(context.Background())
	first := channels[0]
	for i := 0; i < 2; i++ {
		c := createTestChannel(t, store, first.CredentialID, fmt.Sprint("extra", i), "deepseek-flash")
		p.Config.Channels = append(p.Config.Channels, ChannelBinding{c.ID, i + 2, 100, true})
	}
	p.Config.MaxAttempts = 2
	_ = store.SaveConfig(context.Background(), "admin", p.ID, p.Name, p.Revision, p.Config)
	p, _ = store.Policy(context.Background(), p.ID)
	app, _ := NewServer(store, "http://localhost:8090", t.TempDir())
	calls := 0
	app.Engine.Client = &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	res, err := runTestAudit(t, app, p, key.ID, "production", "hello")
	if err == nil || calls != 2 || res.AttemptCount != 2 {
		t.Fatal("attempt limit ignored", calls, err)
	}
	app.Engine.Client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	_, chs, _ := store.RouteSnapshot(context.Background(), p.ID, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	before := calls
	_, err = app.runAudit(ctx, p, chs, key.ID, "production", "timeout", "")
	if err == nil || calls != before+1 {
		t.Fatal("cancellation launched fallback", calls, err)
	}
}

func TestChannelCacheIsolationRotationAndReconciliation(t *testing.T) {
	store := testStore(t)
	p, key := configureBillingPolicy(t, store, 60)
	ctx := context.Background()
	channels, _ := store.Channels(ctx)
	first := channels[0]
	second := createTestChannel(t, store, first.CredentialID, "same model different channel", first.Model)
	p.Config.Channels = append(p.Config.Channels, ChannelBinding{second.ID, 2, 100, true})
	if err := store.SaveConfig(ctx, "admin", p.ID, p.Name, p.Revision, p.Config); err != nil {
		t.Fatal(err)
	}
	p, _ = store.Policy(ctx, p.ID)
	app, _ := NewServer(store, "http://localhost:8090", t.TempDir())
	calls := 0
	app.Engine.Client = &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"model":"resolved-upstream-model","choices":[{"finish_reason":"stop","message":{"content":"{\"confidence\":0.1,\"reason\":\"正常\"}"}}]}`))}, nil
	})}
	a, err := runTestAudit(t, app, p, key.ID, "production", "cache-scope")
	if err != nil {
		t.Fatal(err)
	}
	b, err := runTestAudit(t, app, p, key.ID, "production", "cache-scope")
	if err != nil || !b.CacheHit || calls != 1 || b.ActualModel != "resolved-upstream-model" {
		t.Fatal("cache lost actual model", b, err)
	}
	// A different channel with the exact same model must make its own call.
	p.Config.Channels[0].Enabled = false
	c, err := runTestAudit(t, app, p, key.ID, "production", "cache-scope")
	if err != nil || c.CacheHit || calls != 2 {
		t.Fatal("cross-channel cache reused", c, err)
	}
	p.Config.Channels[0].Enabled = true
	if _, err = store.SaveCredential(ctx, "admin", first.CredentialID, "rotated", "rotated-test-only-key", true); err != nil {
		t.Fatal(err)
	}
	d, err := runTestAudit(t, app, p, key.ID, "production", "cache-scope")
	if err != nil || d.CacheHit || calls != 3 {
		t.Fatal("rotated credential reused cache", d, err)
	}
	// The audit detail must reflect a subsequent per-attempt reconciliation.
	log, err := store.LogDetail(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"amount_cny":"0.01","reason":"test reconciliation"}`))
	req.SetPathValue("id", log.Attempts[0].ID)
	req = req.WithContext(context.WithValue(ctx, sessionKey{}, session{Username: "admin"}))
	if err = app.reconcileCost(httptest.NewRecorder(), req); err != nil {
		t.Fatal(err)
	}
	log, err = store.LogDetail(ctx, a.ID)
	if err != nil || log.Cost.AmountCNY == nil || *log.Cost.AmountCNY != "0.01" || log.Attempts[0].Cost.Status != "reconciled" {
		t.Fatal("stale cost detail", log, err)
	}
}

func TestPolicySettingsRejectInvalidRouting(t *testing.T) {
	for _, change := range []func(*PolicySettings){
		func(c *PolicySettings) { c.Channels = nil },
		func(c *PolicySettings) { c.Channels = []ChannelBinding{{"a", 0, 100, true}} },
		func(c *PolicySettings) { c.Channels = []ChannelBinding{{"a", 1, 0, true}} },
		func(c *PolicySettings) { c.Channels = []ChannelBinding{{"a", 1, 1, true}, {"a", 2, 1, true}} },
		func(c *PolicySettings) { c.MaxAttempts = 0 },
		func(c *PolicySettings) { c.TotalTimeoutMS = 30000 },
	} {
		cfg := DefaultSettings()
		change(&cfg)
		if cfg.Validate() == nil {
			t.Fatal("invalid routing accepted", cfg)
		}
	}
}

func TestLongReasonSurvivesModerationCacheAndLogs(t *testing.T) {
	store := testStore(t)
	p, key := configureBillingPolicy(t, store, 60)
	app, err := NewServer(store, "http://localhost:8090", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reason := strings.Repeat("原因说明", 20) // 80 Unicode characters, not 80 bytes.
	calls := 0
	app.Engine.Client = &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		calls++
		content, _ := json.Marshal(Assessment{.9, reason})
		body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"content": string(content)}}}})
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	})}
	for i := 0; i < 2; i++ {
		res, err := runTestAudit(t, app, p, key.ID, "production", "long reason test")
		if err != nil {
			t.Fatal(err)
		}
		if res.Results[0].Audit.Reason != reason || !res.Results[0].Flagged || res.CacheHit != (i == 1) {
			t.Fatal("reason changed in response", res)
		}
		log, err := store.LogDetail(context.Background(), res.ID)
		if err != nil || log.Reason != reason {
			t.Fatal("reason changed in logs", log, err)
		}
	}
	if calls != 1 {
		t.Fatal("long reason cache missed", calls)
	}
}

func TestInvalidReasonPersistsDetailedErrorAndOutput(t *testing.T) {
	store := testStore(t)
	p, key := configureBillingPolicy(t, store, 0)
	app, err := NewServer(store, "http://localhost:8090", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(Assessment{.9, strings.Repeat("审", 81)})
	app.Engine.Client = &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"content": string(raw)}}}})
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	})}
	response, err := runTestAudit(t, app, p, key.ID, "production", "invalid reason")
	if err == nil {
		t.Fatal("invalid output accepted")
	}
	log, readErr := store.LogDetail(context.Background(), response.ID)
	if readErr != nil {
		t.Fatal(readErr)
	}
	const expected = "reason 为 81 字，超过 80 字上限"
	if log.ErrorCode != "invalid_model_response" || log.ErrorMessage != expected || log.ModelOutput != string(raw) || log.Confidence != nil || log.Flagged {
		t.Fatal("failure evidence missing or treated as verdict", log)
	}
	if len(log.Attempts) != 1 || log.Attempts[0].ErrorMessage != expected || log.Attempts[0].ModelOutput != string(raw) {
		t.Fatal("attempt details missing", log.Attempts)
	}
	items, _, err := store.Logs(context.Background(), LogFilter{Page: 1, PageSize: 20})
	if err != nil || len(items) != 1 || items[0].ErrorMessage != expected || items[0].ModelOutput != "" || items[0].Attempts[0].ModelOutput != "" {
		t.Fatal("list error details or output isolation incorrect", items, err)
	}
}

func TestAuditErrorMessageDoesNotExposeInternalSecrets(t *testing.T) {
	if got := storedAuditError(errors.New("postgres://user:password@host/db")); strings.Contains(got, "password") {
		t.Fatal("internal error exposed", got)
	}
	if got := storedAuditError(problem(502, "invalid_model_response", "response contains sk-test-secret")); strings.Contains(got, "sk-test-secret") {
		t.Fatal("typed error not redacted", got)
	}
}
