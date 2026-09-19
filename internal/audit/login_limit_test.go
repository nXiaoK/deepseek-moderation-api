package audit

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLoginLimitsSeparateTrustedProxyClients(t *testing.T) {
	store := testStore(t)
	config := DefaultRuntimeConfig()
	config.TrustedProxies = []string{"127.0.0.1/32"}
	app, err := NewServer(store, "http://localhost:8090", t.TempDir(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	handler := app.Handler()
	call := func(ip, body string, want int) {
		t.Helper()
		r := httptest.NewRequest("POST", "/admin/auth/login", strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:43210"
		r.Header.Set("Origin", app.Origin)
		r.Header.Set("X-Forwarded-For", ip)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("client %s: got %d want %d: %s", ip, w.Code, want, w.Body.String())
		}
	}
	for range 10 {
		call("198.51.100.1", "{", 400)
	}
	call("198.51.100.1", "{", 429)
	call("198.51.100.2", `{"username":"admin","password":"test-password-123456"}`, 200)
}

func TestSpoofedForwardedHeadersCannotBypassLoginLimit(t *testing.T) {
	for _, trust := range []bool{false, true} {
		t.Run(fmt.Sprintf("proxy_configured=%t", trust), func(t *testing.T) {
			store := testStore(t)
			config := DefaultRuntimeConfig()
			if trust {
				config.TrustedProxies = []string{"127.0.0.1/32"}
			}
			app, err := NewServer(store, "http://localhost:8090", t.TempDir(), config)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(app.Close)
			handler := app.Handler()
			for i := range 15 {
				r := httptest.NewRequest("POST", "/admin/auth/login", strings.NewReader("{"))
				r.RemoteAddr = "203.0.113.1:43210"
				r.Header.Set("Origin", app.Origin)
				r.Header.Set("X-Forwarded-For", fmt.Sprintf("198.51.100.%d", i+1))
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				want := 400
				if i >= 10 {
					want = 429
				}
				if w.Code != want {
					t.Fatalf("request %d: got %d want %d", i, w.Code, want)
				}
			}
			var count int
			if err := store.DB.QueryRow("SELECT count(*) FROM rate_limits WHERE bucket LIKE 'login:%'").Scan(&count); err != nil || count != 1 {
				t.Fatalf("untrusted header created limiter buckets: count=%d err=%v", count, err)
			}
		})
	}
}

func TestLoginGlobalProtectionRejectsBeforeDatabase(t *testing.T) {
	for _, concurrency := range []bool{false, true} {
		t.Run(fmt.Sprintf("concurrency=%t", concurrency), func(t *testing.T) {
			app, err := NewServer(nil, "http://localhost:8090", t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(app.Close)
			count := 120
			if concurrency {
				count = 8
			}
			for i := range count {
				release, err := app.loginIngress.acquire(fmt.Sprint(i), time.Now())
				if err != nil {
					t.Fatal(err)
				}
				if concurrency {
					defer release()
				} else {
					release()
				}
			}
			r := httptest.NewRequest("POST", "/admin/auth/login", strings.NewReader("{"))
			r.Header.Set("Origin", app.Origin)
			w := httptest.NewRecorder()
			app.Handler().ServeHTTP(w, r)
			if w.Code != 429 {
				t.Fatalf("global login protection ignored: %d", w.Code)
			}
		})
	}
}

func TestTrustedProxiesEnvironment(t *testing.T) {
	t.Setenv("AUDIT_TRUSTED_PROXIES", "127.0.0.1/32, ::1/128")
	config, err := RuntimeConfigFromEnv()
	if err != nil || len(config.TrustedProxies) != 2 {
		t.Fatal(config, err)
	}
	t.Setenv("AUDIT_TRUSTED_PROXIES", "0.0.0.0/0")
	if _, err = RuntimeConfigFromEnv(); err == nil {
		t.Fatal("unsafe trusted-proxy configuration accepted")
	}
}
