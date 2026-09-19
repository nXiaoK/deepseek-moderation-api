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

func TestResponsesJSONModeChecksInputMessages(t *testing.T) {
	t.Setenv("AUDIT_SUB2API_ORIGINS", "https://sub2api.test")
	for _, prompt := range []string{InitialPrompt, "只判断网络滥用，正常内容放行。"} {
		for _, scenario := range []string{"text", "image", "image_only", "text_only_channel"} {
			t.Run(scenario+prompt[:6], func(t *testing.T) {
				cfg := grokTestConfig()
				cfg.Model, cfg.Prompt = "gpt-5.6-luna", prompt
				cfg.TextOnly = scenario == "text_only_channel"
				input := "请检查这段内容"
				var images []AuditImage
				if scenario != "text" {
					images = []AuditImage{{URL: "https://example.com/image.png"}}
				}
				if scenario == "image_only" {
					input = ""
				}
				engine := NewEngine(1)
				engine.Client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
					var body struct {
						Instructions string `json:"instructions"`
						Input        []struct {
							Role    string          `json:"role"`
							Content json.RawMessage `json:"content"`
						} `json:"input"`
						Text struct {
							Format struct {
								Type string `json:"type"`
							} `json:"format"`
						} `json:"text"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					if body.Instructions != prompt || body.Text.Format.Type != "json_object" {
						t.Fatal("policy or JSON mode changed")
					}
					// Reproduce gateways that ignore instructions when validating JSON mode.
					foundJSON := false
					for _, message := range body.Input {
						foundJSON = foundJSON || strings.Contains(strings.ToLower(string(message.Content)), "json")
					}
					if !foundJSON {
						return &http.Response{StatusCode: 400, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"detail":"Response input messages must contain the word 'json' in some form to use 'text.format' of type 'json_object'."}`))}, nil
					}
					if len(body.Input) != 2 || body.Input[0].Role != "developer" || body.Input[1].Role != "user" {
						t.Fatal("output contract was not separated from user input")
					}
					if !strings.Contains(string(body.Input[0].Content), "confidence") || !strings.Contains(string(body.Input[0].Content), "reason") {
						t.Fatal("missing audit output fields")
					}
					var userText string
					if scenario == "text" || cfg.TextOnly {
						if json.Unmarshal(body.Input[1].Content, &userText) != nil {
							t.Fatal("text-only content changed")
						}
					} else {
						var content []map[string]string
						if json.Unmarshal(body.Input[1].Content, &content) != nil || len(content) != 2 || content[1]["image_url"] != images[0].URL {
							t.Fatal("image content changed")
						}
						userText = content[0]["text"]
					}
					if userText != "<user_input>"+input+"</user_input>" {
						t.Fatal("audit material was modified")
					}
					return grokStreamResponse(grokSSE(`{"confidence":0.1,"reason":"正常请求"}`, "")), nil
				})}
				if _, _, _, err := engine.Assess(context.Background(), cfg, "test-key", input, images...); err != nil {
					t.Fatal(err)
				}
			})
		}
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
			err := classifyUpstream(&http.Response{StatusCode: 400, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(tt.body))}, "").(*upstreamFailure)
			if err.Code != "upstream_config_invalid" {
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
