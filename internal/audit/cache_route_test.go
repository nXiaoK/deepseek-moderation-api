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
	app.Engine.routeMu.Lock()
	st.until = time.Time{}
	st.inFlight = 0
	app.Engine.routeMu.Unlock()
	if err := store.RevokeKey(context.Background(), "admin", key.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := runTestAudit(t, app, p, key.ID, "production", "cached input"); errorCode(err) != "invalid_api_key" || auditHTTPStatus(err) != http.StatusUnauthorized {
		t.Fatal("revoked caller did not receive authentication error", err)
	}
}

func TestWaitingCachedRouteRechecksCallerAuthorization(t *testing.T) {
	for _, tc := range []struct {
		name, update, code string
		status             int
	}{
		{"disabled", "active=FALSE", "invalid_api_key", http.StatusUnauthorized},
		{"expired", "expires_at=NOW()-INTERVAL '1 second'", "invalid_api_key", http.StatusUnauthorized},
		{"deleted", "deleted_at=NOW()", "invalid_api_key", http.StatusUnauthorized},
		{"policy_removed", "policy_ids='[]'::jsonb", "policy_forbidden", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := testStore(t)
			p, key := configureBillingPolicy(t, store, 60)
			app, err := NewServer(store, "http://localhost:8090", t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			entered, finish := make(chan struct{}, 1), make(chan struct{})
			defer func() {
				select {
				case <-finish:
				default:
					close(finish)
				}
			}()
			var calls atomic.Int32
			app.Engine.Client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
				calls.Add(1)
				entered <- struct{}{}
				select {
				case <-finish:
				case <-r.Context().Done():
					return nil, r.Context().Err()
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(cachedTestResponse))}, nil
			})}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			snapshot, channels, err := store.RouteSnapshot(ctx, p.ID, nil)
			if err != nil {
				t.Fatal(err)
			}
			first, waiting := make(chan error, 1), make(chan error, 1)
			go func() {
				_, err := app.runAudit(ctx, snapshot, channels, key.ID, "production", "same waiting input", "")
				first <- err
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("first request did not reach model")
			}
			go func() {
				_, err := app.runAudit(ctx, snapshot, channels, key.ID, "production", "same waiting input", "")
				waiting <- err
			}()
			for {
				app.Engine.cacheGate.mu.Lock()
				refs := 0
				for _, entry := range app.Engine.cacheGate.entries {
					refs += entry.refs
				}
				app.Engine.cacheGate.mu.Unlock()
				if refs == 2 {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("second request did not wait for cache lock")
				case <-time.After(time.Millisecond):
				}
			}
			if _, err := store.DB.Exec("UPDATE client_api_keys SET "+tc.update+" WHERE id=$1", key.ID); err != nil {
				t.Fatal(err)
			}
			close(finish)
			if err := <-first; err != nil {
				t.Fatal("already sent request did not finish", err)
			}
			if err := <-waiting; errorCode(err) != tc.code || auditHTTPStatus(err) != tc.status {
				t.Fatalf("waiting request got %v, want %d %s", err, tc.status, tc.code)
			}
			if calls.Load() != 1 {
				t.Fatalf("unauthorized caller sent another model request: %d", calls.Load())
			}
			var costs int
			if err := store.DB.QueryRow("SELECT COUNT(*) FROM audit_costs").Scan(&costs); err != nil || costs != 1 {
				t.Fatal("unauthorized cache request created cost record", costs, err)
			}
		})
	}
}

func TestFallbackRechecksCallerPolicyAuthorization(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	p, key := configureBillingPolicy(t, store, 0)
	_, channels, err := store.RouteSnapshot(ctx, p.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	backup := createTestChannel(t, store, channels[0].CredentialID, "fallback", channels[0].Model)
	p.Config.Channels = append(p.Config.Channels, ChannelBinding{backup.ID, 2, 100, true})
	p.Config.MaxAttempts = 2
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
	var calls atomic.Int32
	app.Engine.Client = &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		if _, err := store.DB.Exec("UPDATE client_api_keys SET policy_ids='[]'::jsonb WHERE id=$1", key.ID); err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"temporarily unavailable"}}`))}, nil
	})}
	if _, err := runTestAudit(t, app, p, key.ID, "production", "fallback authorization"); errorCode(err) != "policy_forbidden" || auditHTTPStatus(err) != http.StatusForbidden {
		t.Fatal("fallback ignored withdrawn policy authorization", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("unauthorized fallback reached provider: %d calls", calls.Load())
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
