package audit

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestConfigurableReasonContract(t *testing.T) {
	for _, limit := range []int{1, 80, 125, 200, ReasonLimitCeiling} {
		for _, char := range []string{"审", "🙂", "a"} {
			reason := strings.Repeat(char, limit)
			raw, _ := json.Marshal(Assessment{.5, reason})
			got, err := parseAssessment(raw, limit)
			if err != nil || got.Reason != reason {
				t.Fatal(limit, got, err)
			}
			raw, _ = json.Marshal(Assessment{.5, reason + char})
			_, err = parseAssessment(raw, limit)
			want := fmt.Sprintf("reason 为 %d 字，超过 %d 字上限", limit+1, limit)
			if err == nil || err.Error() != want {
				t.Fatal(limit, err)
			}
		}
		redacted := redactReasonWithLimit(strings.Repeat("审", max(0, limit-6))+" a@b.co", limit)
		if strings.Contains(redacted, "a@b.co") || utf8.RuneCountInString(redacted) > limit {
			t.Fatal(redacted)
		}
	}
	for _, limit := range []int{-1, ReasonLimitCeiling + 1} {
		cfg := DefaultConfig()
		cfg.ReasonMaxChars = limit
		if cfg.Validate() == nil {
			t.Fatal("invalid config accepted", limit)
		}
	}
}

func TestReasonLimitUsedByBothAPIFormats(t *testing.T) {
	for _, format := range []string{APIFormatChatCompletions, APIFormatResponses} {
		for _, images := range [][]AuditImage{nil, {{URL: "https://example.test/image.png"}}} {
			t.Run(fmt.Sprintf("%s/images=%d", format, len(images)), func(t *testing.T) {
				cfg := DefaultConfig()
				cfg.APIFormat, cfg.ReasonMaxChars = format, 200
				reason := strings.Repeat("审", 125)
				engine := NewEngine(1)
				engine.Client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
					var payload map[string]any
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						t.Fatal(err)
					}
					field := "messages"
					if format == APIFormatResponses {
						field = "input"
					}
					messages := payload[field].([]any)
					instruction := messages[0].(map[string]any)["content"].(string)
					if !strings.Contains(instruction, fmt.Sprintf("at most %d Unicode characters", cfg.reasonMaxChars())) {
						t.Fatal(instruction)
					}
					raw, _ := json.Marshal(Assessment{.2, reason})
					if format == APIFormatResponses {
						return grokStreamResponse(grokSSE(string(raw), "")), nil
					}
					body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": string(raw)}}}})
					return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body)), Header: http.Header{}}, nil
				})}
				for _, limit := range []int{200, 80} {
					cfg.ReasonMaxChars = limit
					result, _, _, err := engine.Assess(context.Background(), cfg, "test-key", "hello", images...)
					if limit == 200 && (err != nil || result.Reason != reason) {
						t.Fatal(result, err)
					}
					if limit == 80 && (errorCode(err) != "invalid_model_response" || !strings.Contains(err.Error(), "125 字，超过 80 字上限")) {
						t.Fatal(err)
					}
				}
			})
		}
	}
}

func TestReasonLimitChangesCacheIdentity(t *testing.T) {
	vault, err := NewVault(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	s := &Store{Vault: vault}
	p := Policy{ID: "policy"}
	cfg := DefaultConfig()
	cfg.ReasonMaxChars = 200
	wide := s.assessmentCacheKey("client", p, cfg, 1, "credential", "input")
	cfg.ReasonMaxChars = 80
	narrow := s.assessmentCacheKey("client", p, cfg, 1, "credential", "input")
	if wide == narrow {
		t.Fatal("limit missing from cache fingerprint")
	}
}

func TestAuditSettingsProtectionPersistenceAndRevision(t *testing.T) {
	app, _, call := evaluationTestServer(t)
	const path = "/admin/settings/audit"
	for _, method := range []string{"GET", "PUT"} {
		r := httptest.NewRequest(method, path, strings.NewReader(`{}`))
		w := httptest.NewRecorder()
		app.Handler().ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatal(w.Code)
		}
		if method == "PUT" {
			r = httptest.NewRequest(method, path, strings.NewReader(`{}`))
			r.Header.Set("Cookie", "audit_session=session")
			w = httptest.NewRecorder()
			app.Handler().ServeHTTP(w, r)
			if w.Code != 403 {
				t.Fatal(w.Code)
			}
		}
	}
	var defaults AuditSettings
	if err := json.Unmarshal(call("GET", path, nil, 200), &defaults); err != nil || defaults.ReasonMaxChars != 80 || defaults.Revision != 1 {
		t.Fatal(defaults, err)
	}
	for _, limit := range []any{0, -1, 4097, 1.5, "200", nil} {
		call("PUT", path, map[string]any{"reason_max_chars": limit, "expected_revision": 1}, 400)
	}
	call("PUT", path, map[string]any{"reason_max_chars": 200, "expected_revision": 1}, 200)
	call("PUT", path, map[string]any{"reason_max_chars": 500, "expected_revision": 1}, 409)
	saved, err := app.Store.auditSettings(context.Background())
	if err != nil || saved.ReasonMaxChars != 200 || saved.Revision != 2 {
		t.Fatal(saved, err)
	}
	var actions int
	if err := app.Store.DB.QueryRow("SELECT COUNT(*) FROM admin_action_logs WHERE action='settings.audit'").Scan(&actions); err != nil || actions != 1 {
		t.Fatal(actions, err)
	}
}

func TestReasonSettingUsedByRoutingCacheAndChannelTest(t *testing.T) {
	app, p, call := evaluationTestServer(t)
	p.Config.ResultCacheTTL = 60
	if err := app.Store.SaveConfig(context.Background(), "admin", p.ID, p.Name, p.Revision, p.Config); err != nil {
		t.Fatal(err)
	}
	p, _ = app.Store.Policy(context.Background(), p.ID)
	token, err := app.Store.CreateKey(context.Background(), "admin", "reason tests", []string{p.ID}, 60)
	if err != nil {
		t.Fatal(err)
	}
	key, err := app.Store.AuthenticateKey(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	reason := strings.Repeat("审", 125)
	calls := 0
	app.Engine.Client = &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		calls++
		assessment, _ := json.Marshal(Assessment{.2, reason})
		body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": string(assessment)}}}, "usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}})
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body)), Header: http.Header{}}, nil
	})}
	call("PUT", "/admin/settings/audit", map[string]any{"reason_max_chars": 200, "expected_revision": 1}, 200)
	first, err := runTestAudit(t, app, p, key.ID, "production", "long reason")
	if err != nil || first.Results[0].Audit.Reason != reason {
		t.Fatal(first, err)
	}
	second, err := runTestAudit(t, app, p, key.ID, "production", "long reason")
	if err != nil || !second.CacheHit || second.Results[0].Audit.Reason != reason || calls != 1 {
		t.Fatal(second, calls, err)
	}
	trial, err := runTestAudit(t, app, p, "", "test", "trial long reason")
	if err != nil || trial.Results[0].Audit.Reason != reason {
		t.Fatal(trial, err)
	}
	var stored string
	if err := app.Store.DB.QueryRow("SELECT metadata->>'reason' FROM audit_requests WHERE id=$1", first.ID).Scan(&stored); err != nil || stored != reason {
		t.Fatal(stored, err)
	}
	_, channels, err := app.Store.RouteSnapshot(context.Background(), p.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	var diagnostic ChannelTestResult
	if err := json.Unmarshal(call("POST", "/admin/model-channels/"+channels[0].ID+"/test", map[string]any{"expected_revision": channels[0].Revision}, 200), &diagnostic); err != nil || !diagnostic.OK || diagnostic.Assessment.Reason != reason {
		t.Fatal(diagnostic, err)
	}
	call("PUT", "/admin/settings/audit", map[string]any{"reason_max_chars": 80, "expected_revision": 2}, 200)
	if _, err := runTestAudit(t, app, p, key.ID, "production", "long reason"); errorCode(err) != "invalid_model_response" || !strings.Contains(err.Error(), "125 字，超过 80 字上限") {
		t.Fatal("old cache reused after lowering limit", err)
	}
}
