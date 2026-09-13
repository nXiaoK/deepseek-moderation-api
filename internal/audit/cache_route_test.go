package audit

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const cachedTestResponse = `{"choices":[{"finish_reason":"stop","message":{"content":"{\"confidence\":0.1,\"reason\":\"ok\"}"}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`

func TestCachedRouteWorksDuringCapacityAndCooling(t *testing.T) {
	store := testStore(t)
	p, key := configureBillingPolicy(t, store, 60)
	app, err := NewServer(store, "http://localhost:8090", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	app.Engine.Client = &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(cachedTestResponse))}, nil
	})}
	first, err := runTestAudit(t, app, p, key.ID, "production", "cached input")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < cap(app.Engine.slots); i++ {
		app.Engine.slots <- struct{}{}
	}
	app.Engine.routeMu.Lock()
	st := app.Engine.routes[first.ChannelID]
	st.until = time.Now().Add(time.Hour)
	st.inFlight = 100
	app.Engine.routeMu.Unlock()
	second, err := runTestAudit(t, app, p, key.ID, "production", "cached input")
	if err != nil || !second.CacheHit || calls.Load() != 1 || second.AttemptCount != 0 {
		t.Fatal("cache blocked by upstream state", second, err)
	}
	if app.Engine.channelHealth(first.ChannelID).Status != "cooling" {
		t.Fatal("cache changed circuit state")
	}
	for i := 0; i < cap(app.Engine.slots); i++ {
		<-app.Engine.slots
	}
	if err := store.RevokeKey(context.Background(), "admin", key.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := runTestAudit(t, app, p, key.ID, "production", "cached input"); err == nil {
		t.Fatal("revoked caller reused cache")
	}
}

func TestConcurrentExactCacheCallsAreCoalesced(t *testing.T) {
	store := testStore(t)
	p, key := configureBillingPolicy(t, store, 60)
	app, err := NewServer(store, "http://localhost:8090", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	entered, finish := make(chan struct{}, 1), make(chan struct{})
	var calls atomic.Int32
	app.Engine.Client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-finish:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(cachedTestResponse))}, nil
	})}
	ctx := context.Background()
	snapshot, channels, err := store.RouteSnapshot(ctx, p.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := app.runAudit(ctx, snapshot, channels, key.ID, "production", "same burst", "")
			if err == nil && len(res.Results) != 1 {
				err = fmt.Errorf("missing result")
			}
			errs <- err
		}()
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("model not called")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		app.Engine.cacheGate.mu.Lock()
		refs := 0
		for _, e := range app.Engine.cacheGate.entries {
			refs += e.refs
		}
		app.Engine.cacheGate.mu.Unlock()
		if refs == 12 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	close(finish)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("duplicate model calls: %d", calls.Load())
	}
	var requests, costs int
	if err := store.DB.QueryRow("SELECT COUNT(*) FROM audit_requests").Scan(&requests); err != nil {
		t.Fatal(err)
	}
	if err := store.DB.QueryRow("SELECT COUNT(*) FROM audit_costs").Scan(&costs); err != nil {
		t.Fatal(err)
	}
	if requests != 12 || costs != 12 {
		t.Fatal("each caller must retain accounting", requests, costs)
	}
	app.Engine.cacheGate.mu.Lock()
	defer app.Engine.cacheGate.mu.Unlock()
	if len(app.Engine.cacheGate.entries) != 0 {
		t.Fatal("coalescing entries leaked")
	}
}

func TestKeyedGateCancellation(t *testing.T) {
	var gate keyedGate
	release, _ := gate.acquire(context.Background(), "same")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := gate.acquire(ctx, "same"); err == nil {
		t.Fatal("cancelled waiter acquired")
	}
	release()
	next, err := gate.acquire(context.Background(), "same")
	if err != nil {
		t.Fatal(err)
	}
	next()
	if len(gate.entries) != 0 {
		t.Fatal("gate leaked")
	}
}
