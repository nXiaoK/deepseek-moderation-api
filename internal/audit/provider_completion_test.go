package audit

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestGrokStreamRequiresCompletedEvent(t *testing.T) {
	t.Setenv("AUDIT_SUB2API_ORIGINS", "https://sub2api.test")
	verdict := `{"confidence":0,"reason":"partial"}`
	delta, _ := json.Marshal(map[string]any{"type": "response.output_text.delta", "delta": verdict})
	prefix := "data: " + string(delta) + "\n\n"
	for _, tc := range []struct {
		name, tail string
		valid      bool
	}{
		{"eof after delta", "", false},
		{"done sentinel only", "data: [DONE]\n\n", false},
		{"text done only", "data: {\"type\":\"response.output_text.done\"}\n\n", false},
		{"missing completed status", "data: {\"type\":\"response.completed\",\"response\":{}}\n\n", false},
		{"incomplete despite completed type", "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"incomplete\"}}\n\n", false},
		{"envelope without terminal event", "data: {\"status\":\"completed\"}\n\n", false},
		{"completed", "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n", true},
		{"event field completed", "event: response.completed\ndata: {\"response\":{\"status\":\"completed\"}}\n\n", true},
		{"completed at eof", "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}", true},
		// sub2api v0.2.14 forwards response.done as a successful terminal too.
		{"response done", "data: {\"type\":\"response.done\",\"response\":{\"status\":\"completed\"}}\n\n", true},
		{"event field response done", "event: response.done\ndata: {\"response\":{\"status\":\"completed\"}}\n\n", true},
		{"response done at eof", "data: {\"type\":\"response.done\",\"response\":{\"status\":\"completed\"}}", true},
		{"response done without status", "data: {\"type\":\"response.done\",\"response\":{}}\n\n", false},
		{"response done incomplete", "data: {\"type\":\"response.done\",\"response\":{\"status\":\"incomplete\"}}\n\n", false},
		{"response done failed", "data: {\"type\":\"response.done\",\"response\":{\"status\":\"failed\"}}\n\n", false},
		{"response done with error", "data: {\"type\":\"response.done\",\"response\":{\"status\":\"completed\",\"error\":{\"message\":\"failed\"}}}\n\n", false},
		{"completed with error", "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"error\":{\"message\":\"failed\"}}}\n\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine := NewEngine(1)
			engine.Client = &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
				return grokStreamResponse(prefix + tc.tail), nil
			})}
			assessment, usage, output, err := engine.Assess(context.Background(), grokTestConfig(), "test-key", "test")
			if !usage.Attempted {
				t.Fatal("sent call must remain billable even when truncated")
			}
			if tc.valid {
				if err != nil || assessment.Confidence != 0 || !strings.Contains(output, "partial") {
					t.Fatalf("completed response rejected: %v", err)
				}
			} else if err == nil || errorCode(err) != "invalid_model_response" || !retryable(err) {
				t.Fatalf("unfinished response must fail and allow fallback: %v", err)
			}
		})
	}
}

// The HTTP Responses gateway preserves this terminal frame instead of renaming
// it to response.completed (sub2api v0.2.14, openai_gateway_response_handling.go).
func TestSub2APIResponseDonePreservesOutputAndUsage(t *testing.T) {
	t.Setenv("AUDIT_SUB2API_ORIGINS", "https://sub2api.test")
	const body = `event: response.done
data: {"type":"response.done","response":{"id":"resp_done","model":"custom-grok-alias","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"{\"confidence\":0.8,\"reason\":\"审核原因\"}"}]}],"usage":{"input_tokens":32,"output_tokens":103,"total_tokens":135,"input_tokens_details":{"cached_tokens":20},"output_tokens_details":{"reasoning_tokens":94}}}}

`
	engine := NewEngine(1)
	engine.Client = &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		return grokStreamResponse(body), nil
	})}
	assessment, usage, output, err := engine.Assess(context.Background(), grokTestConfig(), "test-key", "test")
	if err != nil || assessment.Confidence != .8 || assessment.Reason != "审核原因" || !strings.Contains(output, "审核原因") {
		t.Fatalf("sub2api terminal output lost: assessment=%+v output=%q err=%v", assessment, output, err)
	}
	if !usage.Attempted || !usage.Reported || usage.UpstreamRequestID != "resp_done" || usage.ActualModel != "custom-grok-alias" || usage.PromptTokens != 32 || usage.CompletionTokens != 103 || usage.TotalTokens != 135 || usage.ReasoningTokens != 94 || usage.CacheHitTokens == nil || *usage.CacheHitTokens != 20 || usage.CacheMissTokens == nil || *usage.CacheMissTokens != 12 {
		t.Fatalf("sub2api terminal usage lost: %+v", usage)
	}
}
