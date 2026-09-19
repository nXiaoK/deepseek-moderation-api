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
