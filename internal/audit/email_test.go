package audit

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime/quotedprintable"
	"net"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"net/textproto"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func emailTestConfig() EmailSettings {
	return EmailSettings{Enabled: true, Host: "smtp.example.com", Port: 465, Security: "tls", Username: "sender@example.com", From: "sender@example.com", To: "admin@example.com"}
}

func emailTestSave(t *testing.T, app *Server, call func(string, string, any, int) []byte, cfg EmailSettings, password string) {
	t.Helper()
	old, _, err := app.Store.emailSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(cfg)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	body["expected_revision"], body["password"] = old.Revision, password
	call("PUT", "/admin/settings/email", body, 200)
}

func TestEmailSettingsValidation(t *testing.T) {
	for _, change := range []func(*EmailSettings){
		func(c *EmailSettings) { c.Host = "smtp.example.com\r\nMAIL FROM:bad" },
		func(c *EmailSettings) { c.Host = "https://smtp.example.com" },
		func(c *EmailSettings) { c.Port = 0 },
		func(c *EmailSettings) { c.Security = "none" },
		func(c *EmailSettings) { c.To = "one@example.com,two@example.com" },
		func(c *EmailSettings) { c.From = "sender@example.com\r\nBcc: bad@example.com" },
		func(c *EmailSettings) { c.From = "发件人@example.com" },
	} {
		cfg := emailTestConfig()
		change(&cfg)
		if cfg.validate(true) == nil {
			t.Fatalf("accepted invalid settings: %+v", cfg)
		}
	}
}

func TestEmailSettingsProtectionAndRevision(t *testing.T) {
	app, _, call := evaluationTestServer(t)
	const path = "/admin/settings/email"
	for _, route := range []struct{ method, path string }{{"GET", path}, {"PUT", path}, {"POST", path + "/test"}, {"GET", path + "/status"}} {
		r := httptest.NewRequest(route.method, route.path, strings.NewReader(`{}`))
		w := httptest.NewRecorder()
		app.Handler().ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatal(w.Code)
		}
		if route.method != "GET" {
			r = httptest.NewRequest(route.method, route.path, strings.NewReader(`{}`))
			r.Header.Set("Cookie", "audit_session=session")
			w = httptest.NewRecorder()
			app.Handler().ServeHTTP(w, r)
			if w.Code != 403 {
				t.Fatal(w.Code)
			}
		}
	}
	cfg := emailTestConfig()
	emailTestSave(t, app, call, cfg, "test-smtp-secret")
	response := call("GET", path, nil, 200)
	if bytes.Contains(response, []byte("test-smtp-secret")) || !bytes.Contains(response, []byte(`"password_set":true`)) {
		t.Fatal(string(response))
	}
	saved, cipher, err := app.Store.emailSettings(context.Background())
	if err != nil || bytes.Contains(cipher, []byte("test-smtp-secret")) {
		t.Fatal("password not encrypted", err)
	}
	plaintext, err := app.Store.Vault.Open(cipher, "email:smtp-password")
	if err != nil || plaintext != "test-smtp-secret" {
		t.Fatal("cannot decrypt saved password", err)
	}
	emailTestSave(t, app, call, cfg, "")
	_, retained, _ := app.Store.emailSettings(context.Background())
	if !bytes.Equal(cipher, retained) {
		t.Fatal("blank password did not preserve secret")
	}
	raw, _ := json.Marshal(cfg)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	body["expected_revision"] = saved.Revision
	call("PUT", path, body, 409)
	body["expected_revision"] = saved.Revision + 1
	body["host"] = "different.example.com"
	call("PUT", path, body, 400)
	body["host"], body["enabled"], body["clear_password"] = cfg.Host, false, true
	call("PUT", path, body, 200)
	_, cleared, _ := app.Store.emailSettings(context.Background())
	if len(cleared) != 0 {
		t.Fatal("clear password retained ciphertext")
	}
	var actions string
	if err := app.Store.DB.QueryRow("SELECT string_agg(details::text,'') FROM admin_action_logs WHERE action='settings.email'").Scan(&actions); err != nil || strings.Contains(actions, "test-smtp-secret") {
		t.Fatal("secret in action log", err)
	}
}

func TestEmailQueueScopeRetryAndRetention(t *testing.T) {
	app, _, call := evaluationTestServer(t)
	cfg := emailTestConfig()
	confidence := .9
	record := func(id, kind string, flagged, cache, ignored bool, code string) {
		t.Helper()
		l := AuditLog{ID: id, Kind: kind, Flagged: flagged, CacheHit: cache, KeywordIgnored: ignored, ErrorCode: code, Confidence: &confidence, Threshold: .8, Reason: "测试原因 user@example.com", CreatedAt: time.Now(), InputStored: true}
		if err := app.Store.Record(context.Background(), l, "private raw input", 1); err != nil {
			t.Fatal(err)
		}
	}
	count := func(want int) {
		t.Helper()
		var n int
		if err := app.Store.DB.QueryRow("SELECT COUNT(*) FROM email_notifications").Scan(&n); err != nil || n != want {
			t.Fatal(n, want, err)
		}
	}
	record("disabled", "production", true, false, false, "")
	count(0)
	emailTestSave(t, app, call, cfg, "test-secret")
	record("hit", "production", true, false, false, "")
	record("cached-hit", "production", true, true, false, "")
	record("allow", "production", false, false, false, "")
	record("trial", "test", true, false, false, "")
	record("evaluation", "evaluation", true, false, false, "")
	record("error", "production", true, false, false, "upstream_unavailable")
	record("ignored", "production", true, false, true, "")
	count(2)
	if err := app.Store.Record(context.Background(), AuditLog{ID: "hit", Kind: "production", Flagged: true}, "", 1); err == nil {
		t.Fatal("duplicate record accepted")
	}
	count(2)
	failing := func(context.Context, EmailSettings, string, string, string) error {
		return errors.New("secret SMTP error")
	}
	worked, err := app.deliverEmail(context.Background(), failing)
	if err != nil || !worked {
		t.Fatal(worked, err)
	}
	var attemptedID, code string
	var next time.Time
	if err := app.Store.DB.QueryRow("SELECT request_id,last_error,next_attempt_at FROM email_notifications WHERE attempts=1").Scan(&attemptedID, &code, &next); err != nil || code != "email_send_failed" || !next.After(time.Now()) {
		t.Fatal(code, next, err)
	}
	success := func(_ context.Context, c EmailSettings, password, subject, body string) error {
		if c.To != cfg.To || password != "test-secret" || !strings.Contains(subject, "命中") || !strings.Contains(body, "[隐去]") || strings.Contains(body, "private raw input") || strings.Contains(body, "user@example.com") {
			t.Error("unsafe or incomplete email")
		}
		return nil
	}
	worked, err = app.deliverEmail(context.Background(), success)
	if err != nil || !worked {
		t.Fatal(worked, err)
	}
	for attempt := 2; attempt <= 5; attempt++ {
		if _, err := app.Store.DB.Exec("UPDATE email_notifications SET next_attempt_at=NOW() WHERE request_id=$1", attemptedID); err != nil {
			t.Fatal(err)
		}
		worked, err = app.deliverEmail(context.Background(), failing)
		if err != nil || !worked {
			t.Fatal(worked, err)
		}
	}
	worked, err = app.deliverEmail(context.Background(), success)
	if err != nil || worked {
		t.Fatal("retry limit ignored", worked, err)
	}
	status := call("GET", "/admin/settings/email/status", nil, 200)
	if !bytes.Contains(status, []byte(`"sent":1`)) || !bytes.Contains(status, []byte(`"failed":1`)) {
		t.Fatal(string(status))
	}
	cfg.Enabled = false
	emailTestSave(t, app, call, cfg, "")
	count(1)
	if _, err := app.Store.DB.Exec("DELETE FROM audit_requests"); err != nil {
		t.Fatal(err)
	}
	count(0)
}

func TestEmailQueueConcurrentClaimsAndRestart(t *testing.T) {
	app, _, call := evaluationTestServer(t)
	emailTestSave(t, app, call, emailTestConfig(), "test-secret")
	if err := app.Store.Record(context.Background(), AuditLog{ID: "restart-hit", Kind: "production", Flagged: true}, "", 1); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewServer(app.Store, app.Origin, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	var sends atomic.Int32
	sender := func(context.Context, EmailSettings, string, string, string) error { sends.Add(1); return nil }
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := restarted.deliverEmail(context.Background(), sender); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if sends.Load() != 1 {
		t.Fatal("duplicate delivery", sends.Load())
	}
}

func TestEmailMessageAndTransportSecurity(t *testing.T) {
	cfg := emailTestConfig()
	message, err := mail.ReadMessage(bytes.NewReader(emailMessage(cfg, "测试通知", "中文内容\n第二行")))
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(quotedprintable.NewReader(message.Body))
	if err != nil || string(body) != "中文内容\r\n第二行" {
		t.Fatal(string(body), err)
	}
	// A server without STARTTLS must never receive AUTH or a message.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		_, _ = io.WriteString(conn, "220 localhost SMTP\r\n")
		buf := make([]byte, 1024)
		_, _ = conn.Read(buf)
		_, _ = io.WriteString(conn, "250 localhost\r\n")
		n, _ := conn.Read(buf)
		if strings.Contains(string(buf[:n]), "AUTH") {
			t.Error("authentication sent without TLS")
		}
		_, _ = io.WriteString(conn, "500 STARTTLS unavailable\r\n")
	}()
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	cfg.Host, cfg.Port, cfg.Security = host, mustEmailPort(t, port), "starttls"
	if err := sendSMTP(context.Background(), cfg, "test-secret", "subject", "body"); err == nil {
		t.Fatal("accepted unencrypted server")
	}
	<-done
	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer tlsServer.Close()
	cfg.Host, port, _ = net.SplitHostPort(tlsServer.Listener.Addr().String())
	cfg.Port, cfg.Security = mustEmailPort(t, port), "tls"
	if err := sendSMTP(context.Background(), cfg, "test-secret", "subject", "body"); err == nil {
		t.Fatal("accepted untrusted TLS certificate")
	}
}

func mustEmailPort(t *testing.T, port string) int {
	t.Helper()
	n, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestEmailSMTPDelivery(t *testing.T) {
	// Use a local trusted test certificate; production still uses system roots.
	certificateServer := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer certificateServer.Close()
	roots := x509.NewCertPool()
	roots.AddCert(certificateServer.Certificate())
	for _, security := range []string{"tls", "starttls"} {
		t.Run(security, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			result := make(chan error, 1)
			go func() {
				result <- func() error {
					conn, err := listener.Accept()
					if err != nil {
						return err
					}
					defer conn.Close()
					_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
					if security == "tls" {
						conn = tls.Server(conn, certificateServer.TLS.Clone())
					}
					wire := textproto.NewConn(conn)
					defer wire.Close()
					if err := wire.PrintfLine("220 localhost SMTP"); err != nil {
						return err
					}
					if line, err := wire.ReadLine(); err != nil || !strings.HasPrefix(line, "EHLO ") {
						return errors.New("expected EHLO")
					}
					if security == "starttls" {
						_ = wire.PrintfLine("250-localhost\r\n250 STARTTLS")
						if line, err := wire.ReadLine(); err != nil || line != "STARTTLS" {
							return errors.New("expected STARTTLS")
						}
						_ = wire.PrintfLine("220 Ready for TLS")
						conn = tls.Server(conn, certificateServer.TLS.Clone())
						wire = textproto.NewConn(conn)
						defer wire.Close()
						if line, err := wire.ReadLine(); err != nil || !strings.HasPrefix(line, "EHLO ") {
							return errors.New("expected EHLO after TLS")
						}
					}
					_ = wire.PrintfLine("250-localhost\r\n250 AUTH PLAIN")
					line, err := wire.ReadLine()
					if err != nil || !strings.HasPrefix(line, "AUTH PLAIN ") {
						return errors.New("expected authentication")
					}
					decoded, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(line, "AUTH PLAIN "))
					if string(decoded) != "\x00sender@example.com\x00test-secret" {
						return errors.New("incorrect authentication")
					}
					_ = wire.PrintfLine("235 Authenticated")
					for _, want := range []string{"MAIL FROM:<sender@example.com>", "RCPT TO:<admin@example.com>", "DATA"} {
						line, err := wire.ReadLine()
						if err != nil || line != want {
							return errors.New("incorrect envelope or data command: " + line)
						}
						if want == "DATA" {
							_ = wire.PrintfLine("354 Send message")
						} else {
							_ = wire.PrintfLine("250 OK")
						}
					}
					raw, err := wire.ReadDotBytes()
					if err != nil {
						return err
					}
					message, err := mail.ReadMessage(bytes.NewReader(raw))
					if err != nil {
						return err
					}
					body, err := io.ReadAll(quotedprintable.NewReader(message.Body))
					if err != nil || string(body) != "测试正文\n" {
						return errors.New("incorrect message body: " + string(body))
					}
					_ = wire.PrintfLine("250 Accepted")
					line, err = wire.ReadLine()
					if err != nil || line != "QUIT" {
						return errors.New("expected QUIT")
					}
					return wire.PrintfLine("221 Bye")
				}()
			}()
			cfg := emailTestConfig()
			host, port, _ := net.SplitHostPort(listener.Addr().String())
			cfg.Host, cfg.Port, cfg.Security = host, mustEmailPort(t, port), security
			err = sendSMTPWithTLS(context.Background(), cfg, "test-secret", "测试邮件", "测试正文", &tls.Config{ServerName: host, RootCAs: roots, MinVersion: tls.VersionTLS12})
			if err != nil {
				t.Fatal(err)
			}
			if err := <-result; err != nil {
				t.Fatal(err)
			}
		})
	}
}
