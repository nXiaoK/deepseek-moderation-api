package audit

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// Reduced Cline response: retain its envelope, model output and usage structure,
// without copying account-specific request IDs or gateway routing metadata.
const clineChatCompletion = `{
  "id":"test-generation",
  "model":"deepseek/deepseek-v4.1-flash",
  "object":"chat.completion",
  "choices":[{"index":0,"finish_reason":"stop","message":{
    "role":"assistant",
    "content":"{\"confidence\":0.0,\"reason\":\"正常连接测试消息，无违法或滥用内容。\"}",
    "reasoning":"Provider reasoning metadata",
    "provider_metadata":{"gateway":{"cost":"0.0001713"}}
  }}],
  "usage":{"prompt_tokens":103,"completion_tokens":117,"total_tokens":220,
    "completion_tokens_details":{"reasoning_tokens":92}}
}`

func TestChatCompletionDataEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"native", clineChatCompletion, true},
		{"cline", `{"data":` + clineChatCompletion + `,"success":true}`, true},
		{"failed_envelope", `{"data":` + clineChatCompletion + `,"success":false}`, false},
		{"missing_success", `{"data":` + clineChatCompletion + `}`, false},
		{"string_success", `{"data":` + clineChatCompletion + `,"success":"true"}`, false},
		{"null_data", `{"data":null,"success":true}`, false},
		{"array_data", `{"data":[` + clineChatCompletion + `],"success":true}`, false},
		{"native_empty_choices", `{"choices":[],"data":` + clineChatCompletion + `,"success":true}`, false},
		{"native_null_choices", `{"choices":null,"data":` + clineChatCompletion + `,"success":true}`, false},
		{"duplicate_success", `{"data":` + clineChatCompletion + `,"success":false,"success":true}`, false},
		{"duplicate_nested_choices", `{"success":true,"data":` + strings.Replace(clineChatCompletion, `"choices":`, `"choices":[],"choices":`, 1) + `}`, false},
		{"trailing_data", `{"data":` + clineChatCompletion + `,"success":true}{}`, false},
		{"incomplete", `{"data":` + strings.Replace(clineChatCompletion, `"finish_reason":"stop"`, `"finish_reason":"length"`, 1) + `,"success":true}`, false},
		{"invalid_verdict", `{"data":` + strings.Replace(clineChatCompletion, `confidence\":0.0`, `confidence\":2.0`, 1) + `,"success":true}`, false},
		{"refusal", `{"data":` + strings.Replace(clineChatCompletion, `"role":"assistant"`, `"role":"assistant","refusal":"refused"`, 1) + `,"success":true}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.BaseURL, cfg.APIFormat, cfg.Model = "https://api.cline.bot/api/v1", APIFormatChatCompletions, "deepseek/deepseek-v4.1-flash"
			engine := NewEngine(1)
			engine.Client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})}
			assessment, usage, output, err := engine.Assess(context.Background(), cfg, "test-api-key", channelTestInput)
			if !usage.Attempted {
				t.Fatal("sent request lost attempted usage")
			}
			if !tc.valid {
				if err == nil || errorCode(err) != "invalid_model_response" {
					t.Fatal("invalid response accepted", assessment, err)
				}
				return
			}
			if err != nil || assessment.Confidence != 0 || assessment.Reason != "正常连接测试消息，无违法或滥用内容。" || !strings.Contains(output, `"confidence":0.0`) {
				t.Fatal("valid completion rejected", assessment, output, err)
			}
			if !usage.Reported || usage.PromptTokens != 103 || usage.CompletionTokens != 117 || usage.TotalTokens != 220 || usage.ReasoningTokens != 92 || usage.UpstreamRequestID != "test-generation" || usage.ActualModel != cfg.Model {
				t.Fatal("gateway usage or metadata lost", usage)
			}
		})
	}
}
