package audit

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
)

func TestChannelDiagnosticReportsActualFailureAndRecovery(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	const key = "arbitrary/private+test-key="
	cred, err := store.SaveProviderCredential(ctx, "admin", "", "gateway", key, true, ProviderDeepSeek, "https://api.cline.bot/api/v1", APIFormatChatCompletions)
	if err != nil {
		t.Fatal(err)
	}
	c := createTestChannel(t, store, cred, "cline", "~deepseek/deepseek-v4-flash-latest")
	app, err := NewServer(store, "http://localhost:8090", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	if _, err := store.DB.Exec("INSERT INTO admin_sessions(token_hash,username,csrf,expires_at) VALUES($1,'admin','csrf',NOW()+INTERVAL '1 hour')", digest("session")); err != nil {
		t.Fatal(err)
	}
	status := 401
	body := `{"error":{"message":"invalid key ` + key + `"}}`
	var networkErr error
	calls := 0
	app.Engine.Client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != "https://api.cline.bot/api/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer "+key || r.Method != "POST" {
			t.Fatal("wrong request", r.Method, r.URL)
		}
		var payload struct {
			Model    string `json:"model"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.Model != c.Model || len(payload.Messages) != 2 || !strings.Contains(payload.Messages[1].Content, channelTestInput) {
			t.Fatal("wrong test payload", payload)
		}
		if networkErr != nil {
			return nil, networkErr
		}
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	call := func(login, csrf bool, revision int64, want int) ChannelTestResult {
		t.Helper()
		raw, _ := json.Marshal(map[string]int64{"expected_revision": revision})
		r := httptest.NewRequest("POST", "/admin/model-channels/"+c.ID+"/test", strings.NewReader(string(raw)))
		r.Header.Set("Origin", app.Origin)
		if login {
			r.Header.Set("Cookie", "audit_session=session")
		}
		if csrf {
			r.Header.Set("X-CSRF-Token", "csrf")
		}
		w := httptest.NewRecorder()
		app.Handler().ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("got %d want %d: %s", w.Code, want, w.Body.String())
		}
		if strings.Contains(w.Body.String(), key) {
			t.Fatal("API key leaked in diagnostic")
		}
		var result ChannelTestResult
		if want == 200 && json.Unmarshal(w.Body.Bytes(), &result) != nil {
			t.Fatal("invalid result", w.Body.String())
		}
		return result
	}
	call(false, false, c.Revision, 401)
	call(true, false, c.Revision, 403)
	call(true, true, c.Revision+1, 409)
	if calls != 0 {
		t.Fatal("unauthorized or stale test reached upstream")
	}
	for _, tc := range []struct {
		status             int
		body, code, detail string
	}{
		{401, body, "upstream_auth_failed", "invalid key [隐去]"},
		{404, `<html>secret upstream page</html>`, "upstream_config_invalid", "HTTP 404"},
		{400, `{"message":"Model ~deepseek/deepseek-v4-flash-latest not found"}`, "upstream_config_invalid", "Model ~deepseek"},
		{429, `{"error":"Insufficient balance"}`, "upstream_rate_limited", "Insufficient balance"},
		{502, `{"detail":"gateway unavailable"}`, "upstream_unavailable", "gateway unavailable"},
		{200, `not-json`, "invalid_model_response", "JSON"},
		{200, `{"choices":[{"finish_reason":"length","message":{"content":"partial"}}]}`, "invalid_model_response", "finish_reason=length"},
	} {
		status, body = tc.status, tc.body
		result := call(true, true, c.Revision, 200)
		if result.OK || !result.Attempted || result.HTTPStatus != status || result.ErrorCode != tc.code || !strings.Contains(result.ErrorMessage, tc.detail) || result.Hint == "" {
			t.Fatalf("wrong diagnostic: %+v", result)
		}
		if result.Model != c.Model || result.Endpoint != "https://api.cline.bot/api/v1/chat/completions" || result.APIFormat != APIFormatChatCompletions || result.TimeoutMS != c.TimeoutMS {
			t.Fatal("diagnostic metadata missing", result)
		}
		if strings.Contains(result.ErrorMessage, "secret upstream page") {
			t.Fatal("raw HTML leaked")
		}
	}
	networkErr = &net.DNSError{Err: "no such host", Name: "api.cline.bot", IsNotFound: true}
	result := call(true, true, c.Revision, 200)
	if result.HTTPStatus != 0 || !strings.Contains(result.ErrorMessage, "DNS") {
		t.Fatal("DNS detail missing", result)
	}
	networkErr = nil
	status, body = 200, `{"choices":[{"finish_reason":"stop","message":{"content":"{\"confidence\":0.1,\"reason\":\"正常\"}"}}]}`
	before := calls
	for i := 0; i < 2; i++ {
		if i == 1 {
			body = `{"success":true,"data":` + body + `}`
		}
		result = call(true, true, c.Revision, 200)
		if !result.OK || result.HTTPStatus != 200 || result.Assessment == nil || result.Assessment.Confidence != .1 {
			t.Fatal("recovery failed", result)
		}
	}
	if calls != before+2 || !app.currentChannelHealth(c).Verified {
		t.Fatal("test used cache or did not update health")
	}
	// Diagnostics work even without a policy and for a saved disabled channel.
	c.Enabled = false
	c, err = store.SaveChannel(ctx, "admin", c)
	if err != nil {
		t.Fatal(err)
	}
	if result := call(true, true, c.Revision, 200); !result.OK {
		t.Fatal("disabled channel not testable", result)
	}
	for i := 0; i < cap(app.trialSlots); i++ {
		app.trialSlots <- struct{}{}
	}
	before = calls
	result = call(true, true, c.Revision, 200)
	if result.Attempted || result.ErrorCode != "trial_capacity_exceeded" || calls != before {
		t.Fatal("trial limit bypassed", result)
	}
	for i := 0; i < cap(app.trialSlots); i++ {
		<-app.trialSlots
	}
	c.RPM = 1
	c, err = store.SaveChannel(ctx, "admin", c)
	if err != nil {
		t.Fatal(err)
	}
	result = call(true, true, c.Revision, 200)
	if result.Attempted || result.ErrorCode != "channel_rpm_exceeded" || calls != before {
		t.Fatal("shared RPM bypassed", result)
	}
	if _, err := store.SaveProviderCredential(ctx, "admin", cred, "gateway", "", false, ProviderDeepSeek, "https://api.cline.bot/api/v1"); err != nil {
		t.Fatal(err)
	}
	channels, err := store.Channels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c = channels[0]
	result = call(true, true, c.Revision, 200)
	if result.Attempted || result.ErrorCode != "credential_unavailable" || calls != before {
		t.Fatal("disabled credential used", result)
	}
}

func TestChannelDiagnosticResponsesAndOutputRedaction(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	const key = "unusual-private-test-key"
	cred, err := store.SaveProviderCredential(ctx, "admin", "", "responses", key, true, ProviderDeepSeek, "https://gateway.test/api/v1", APIFormatResponses)
	if err != nil {
		t.Fatal(err)
	}
	c := createTestChannel(t, store, cred, "responses", "~deepseek/deepseek-v4-flash-latest")
	app, err := NewServer(store, "http://localhost:8090", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	app.Engine.Client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/v1/responses" {
			t.Fatal("wrong endpoint", r.URL)
		}
		return grokStreamResponse(grokSSE(`{"confidence":0.1,"reason":"`+key+`"}`, "")), nil
	})}
	raw, _ := json.Marshal(map[string]int64{"expected_revision": c.Revision})
	r := httptest.NewRequest("POST", "/admin/model-channels/"+c.ID+"/test", strings.NewReader(string(raw)))
	r.SetPathValue("id", c.ID)
	w := httptest.NewRecorder()
	if err := app.testChannel(w, r); err != nil {
		t.Fatal(err)
	}
	var result ChannelTestResult
	if json.Unmarshal(w.Body.Bytes(), &result) != nil || !result.OK || result.HTTPStatus != 200 || strings.Contains(w.Body.String(), key) || !strings.Contains(result.ModelOutput, "[隐去]") {
		t.Fatal("Responses test or redaction failed", w.Body.String())
	}
}

func TestChannelDiagnosticNetworkErrorsAreSpecificAndSafe(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{context.DeadlineExceeded, "超时"},
		{&net.DNSError{Err: "private-key", Name: "bad.host"}, "DNS"},
		{&tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}, "TLS"},
		{syscall.ECONNREFUSED, "连接被拒绝"},
		{io.ErrUnexpectedEOF, "断开连接"},
		{errors.New("private-key"), "暂时不可用"},
	} {
		err := upstreamCallError(tc.err, DefaultConfig())
		if !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "private-key") {
			t.Fatal(err)
		}
	}
}
