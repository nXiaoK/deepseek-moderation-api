package audit

import (
	"context"
	"fmt"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestIngressRateLimitsAndRefill(t *testing.T) {
	g := ingressGuard{limits: ingressLimits{5, 2, 4, 2, 10}}
	now := time.Now()
	acquire := func(ip string, want string) {
		t.Helper()
		release, err := g.acquire(ip, now)
		if (want == "" && err != nil) || (want != "" && (err == nil || errorCode(err) != want)) {
			t.Fatalf("%s: got %v, want %s", ip, err, want)
		}
		if release != nil {
			release()
			release()
		}
	}
	acquire("a", "")
	acquire("a", "")
	for range 20 {
		acquire("a", "ingress_rate_limited")
	}
	acquire("b", "") // Blocked a did not spend b's global allowance.
	acquire("b", "")
	acquire("c", "")
	acquire("d", "ingress_rate_limited")
	if len(g.clients) != 3 {
		t.Fatal("global rejection allocated a client bucket")
	}
	now = now.Add(30 * time.Second)
	acquire("a", "")
	acquire("a", "ingress_rate_limited")
	if g.global.active != 0 {
		t.Fatal("release leaked or underflowed a slot")
	}
}

func TestIngressConcurrencyAndBoundedClients(t *testing.T) {
	g := ingressGuard{limits: ingressLimits{1000, 100, 2, 1, 2}}
	now := time.Now()
	a, err := g.acquire("a", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = g.acquire("a", now); errorCode(err) != "request_capacity_exceeded" {
		t.Fatal(err)
	}
	b, err := g.acquire("b", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = g.acquire("c", now); errorCode(err) != "ingress_capacity_exceeded" {
		t.Fatal(err)
	}
	// Active clients remain protected even when old enough for idle eviction.
	if _, err = g.acquire("c", now.Add(time.Minute)); errorCode(err) != "ingress_capacity_exceeded" {
		t.Fatal(err)
	}
	a()
	b()
	c, err := g.acquire("c", now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	c()
	if len(g.clients) != 1 {
		t.Fatalf("idle buckets not reclaimed: %d", len(g.clients))
	}
}

func TestIngressConcurrentRelease(t *testing.T) {
	g := ingressGuard{limits: ingressLimits{10000, 1000, 8, 2, 16}}
	var wg sync.WaitGroup
	for i := range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := g.acquire(fmt.Sprint(i%10), time.Now())
			if err != nil {
				if errorCode(err) != "request_capacity_exceeded" {
					t.Errorf("unexpected rejection: %v", err)
				}
				return
			}
			release()
			release()
		}()
	}
	wg.Wait()
	if g.global.active != 0 {
		t.Fatal("concurrent requests leaked slots")
	}
}

func TestAnonymousAdmissionRejectsBeforeDatabase(t *testing.T) {
	config := DefaultRuntimeConfig()
	config.RequestConcurrency = 1
	// Deliberately no Store: both rejection paths must perform no database I/O.
	app, err := NewServer(nil, "http://localhost:8090", t.TempDir(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	release, err := app.admission.acquire(0, moderationImageBodyLimit)
	if err != nil {
		t.Fatal(err)
	}
	handler := app.Handler()
	for range 25 {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("POST", "/v1/moderations", nil))
		if w.Code != 503 || w.Header().Get("X-Audit-Request-ID") != "" {
			t.Fatalf("anonymous traffic bypassed capacity: %d %s", w.Code, w.Body.String())
		}
	}
	release()
	r := httptest.NewRequest("POST", "/v1/moderations", nil)
	r.ContentLength = moderationImageBodyLimit + 1
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 413 || app.ingress.rejected != 26 {
		t.Fatalf("oversized rejection: status=%d count=%d", w.Code, app.ingress.rejected)
	}
}

func TestAnonymousRequestsHaveBoundedPersistentLogs(t *testing.T) {
	store := testStore(t)
	config := DefaultRuntimeConfig()
	config.IngressIPRPM, config.IngressRPM = 3, 5
	app, err := NewServer(store, "http://localhost:8090", t.TempDir(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	handler := app.Handler()
	for i := range 25 {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/v1/moderations", nil)
		want := 401
		if i >= 3 {
			want = 429
		}
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("request %d: got %d want %d", i, w.Code, want)
		}
	}
	var count int
	if err = store.DB.QueryRowContext(context.Background(), "SELECT count(*) FROM audit_requests WHERE metadata->'request'->>'stage'='authentication'").Scan(&count); err != nil || count != 3 {
		t.Fatalf("unbounded or missing authentication failure logs: count=%d err=%v", count, err)
	}
	if app.ingress.rejected != 22 {
		t.Fatalf("rejected requests not counted: %d", app.ingress.rejected)
	}
}
