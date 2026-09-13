package audit

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestThirdPartyGPTDoesNotReceiveTemperature(t *testing.T) {
	t.Setenv("AUDIT_SUB2API_ORIGINS", "https://sub2api.test")
	for _, provider := range []string{ProviderDeepSeek, ProviderGrok} {
		t.Run(provider, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Provider, cfg.BaseURL, cfg.Model = provider, "https://sub2api.test", "gpt-5.6-luna"
			engine := NewEngine(1)
			engine.Client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
				var payload map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				if _, exists := payload["temperature"]; exists {
					t.Fatal("temperature still sent to GPT backend")
				}
				var model string
				_ = json.Unmarshal(payload["model"], &model)
				if model != "gpt-5.6-luna" {
					t.Fatal("requested model changed")
				}
				if provider == ProviderGrok {
					if r.URL.Path != "/v1/responses" {
						t.Fatal("wrong Responses endpoint")
					}
					return grokStreamResponse(grokSSE(`{"confidence":0.1,"reason":"正常请求"}`, "")), nil
				}
				if r.URL.Path != "/v1/chat/completions" {
					t.Fatal("wrong Chat Completions endpoint")
				}
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"choices":[{"finish_reason":"stop","message":{"content":"{\"confidence\":0.1,\"reason\":\"正常请求\"}"}}]}`))}, nil
			})}
			if _, _, _, err := engine.Assess(context.Background(), cfg, "test-key", "hello"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUpstreamErrorDetail(t *testing.T) {
	for _, tt := range []struct{ name, body, want string }{
		{"fastapi", `{"detail":"Unsupported parameter: temperature"}`, "Unsupported parameter: temperature"},
		{"openai", `{"error":{"message":"Unsupported parameter: max_output_tokens","param":"max_output_tokens"}}`, "Unsupported parameter: max_output_tokens"},
		{"redacted", `{"detail":"Bad sk-secret dsa_private bearer another-secret"}`, "[隐去]"},
		{"html", `<html>private server response</html>`, ""},
		{"unrelated_fields", `{"request":{"api_key":"private"},"detail":{"private":"data"}}`, ""},
		{"too_large", `{"detail":"` + strings.Repeat("x", 8192) + `"}`, ""},
		{"bounded", `{"detail":"` + strings.Repeat("错", 1000) + `"}`, strings.Repeat("错", 512) + "…"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := classifyUpstream(&http.Response{StatusCode: 400, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(tt.body))}).(*upstreamFailure)
			if err.Code != "upstream_config_invalid" || !err.permanent {
				t.Fatal("error classification changed")
			}
			if tt.want != "" && !strings.Contains(err.Message, tt.want) {
				t.Fatalf("lost upstream reason: %s", err.Message)
			}
			if tt.want == "" && strings.Contains(err.Message, "HTTP") {
				t.Fatal("unstructured response was exposed")
			}
			for _, secret := range []string{"sk-secret", "dsa_private", "another-secret", "private server response"} {
				if strings.Contains(err.Message, secret) {
					t.Fatal("upstream secret leaked")
				}
			}
			if utf8.RuneCountInString(err.Message) > 600 {
				t.Fatal("unbounded error message")
			}
		})
	}
}
