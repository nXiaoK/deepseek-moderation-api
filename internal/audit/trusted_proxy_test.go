package audit

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestClientIPTrustBoundary(t *testing.T) {
	trusted, err := parseTrustedProxies([]string{"127.0.0.1/32", "::1/128", "10.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, peer, forwarded, want string
	}{
		{"direct spoof", "203.0.113.1:1234", "198.51.100.1", "203.0.113.1"},
		{"trusted IPv4", "127.0.0.1:1234", "198.51.100.1", "198.51.100.1"},
		{"trusted IPv6", "[::1]:1234", "2001:db8::1", "2001:db8::1"},
		{"mapped IPv4", "[::ffff:127.0.0.1]:1234", "::ffff:198.51.100.1", "198.51.100.1"},
		{"trusted chain", "127.0.0.1:1234", "198.51.100.1, 10.1.2.3", "198.51.100.1"},
		{"forged prefix", "127.0.0.1:1234", "198.51.100.1, 203.0.113.5, 10.1.2.3", "203.0.113.5"},
		{"invalid chain", "127.0.0.1:1234", "198.51.100.1, invalid", "127.0.0.1"},
		{"empty chain item", "127.0.0.1:1234", "198.51.100.1,", "127.0.0.1"},
		{"no header", "127.0.0.1:1234", "", "127.0.0.1"},
		{"too many hops", "127.0.0.1:1234", strings.Repeat("10.1.2.3,", 32) + "198.51.100.1", "127.0.0.1"},
		{"invalid peer", "malformed", "198.51.100.1", "unknown"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/admin/auth/login", nil)
			r.RemoteAddr = tt.peer
			r.Header.Set("X-Forwarded-For", tt.forwarded)
			if got := requestClientIP(r, trusted); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
	r := httptest.NewRequest("POST", "/", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("X-Forwarded-For", "198.51.100.1")
	if got := requestClientIP(r, nil); got != "127.0.0.1" {
		t.Fatal("forwarded headers trusted without explicit configuration", got)
	}
	r.Header.Add("X-Forwarded-For", "203.0.113.5")
	if got := requestClientIP(r, trusted); got != "203.0.113.5" {
		t.Fatal("multiple header fields bypassed hop order", got)
	}
}

func TestTrustedProxyConfigValidation(t *testing.T) {
	for _, value := range []string{"", "localhost", "*", "0.0.0.0/0", "::/0", "::ffff:0.0.0.0/96", "127.0.0.1/33", "127.0.0.1:80", "fe80::1%en0"} {
		if _, err := parseTrustedProxies([]string{value}); err == nil {
			t.Errorf("invalid trusted proxy accepted: %s", value)
		}
	}
	if _, err := parseTrustedProxies([]string{"127.0.0.1", "::1", "::ffff:127.0.0.1/128"}); err != nil {
		t.Fatal(err)
	}
}
