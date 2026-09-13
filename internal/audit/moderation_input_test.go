package audit

import (
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestModerationImageTextExtraction(t *testing.T) {
	// An 800 KiB file already exceeds 1 MiB when encoded for sub2api.
	image := "data:image/png;base64," + base64.StdEncoding.EncodeToString(make([]byte, 800*1024))
	mixed := func(text, image string) string {
		raw, _ := json.Marshal(map[string]any{"model": "abuse-audit-v1", "input": []any{
			map[string]any{"type": "text", "text": text},
			map[string]any{"type": "image_url", "image_url": map[string]string{"url": image}},
			map[string]any{"type": "text", "text": ""},
		}})
		return string(raw)
	}
	base := mixed("inspect screenshot", "https://example.com/image.png")
	for _, tt := range []struct {
		name, body, wantText, wantCode string
		wantFallback                   bool
	}{
		{"base64_800KiB", mixed("inspect screenshot", image), "inspect screenshot\n", "", true},
		{"small_image_url", base, "inspect screenshot\n", "", true},
		{"exact_limit", base + strings.Repeat(" ", moderationTextBodyLimit-len(base)), "inspect screenshot\n", "", true},
		{"above_limit", base + strings.Repeat(" ", moderationTextBodyLimit-len(base)+1), "inspect screenshot\n", "", true},
		{"empty_text", mixed(" ", image), "", "empty_input", true},
		{"long_text", mixed(strings.Repeat("审", 64001), image), "", "input_too_large", true},
		{"max_text", mixed(strings.Repeat("审", 63999), image), strings.Repeat("审", 63999) + "\n", "", true},
		{"text_only", `{"model":"abuse-audit-v1","input":"hello"}`, "hello", "", false},
		{"oversized_text", `{"model":"abuse-audit-v1","input":"` + strings.Repeat("a", moderationTextBodyLimit) + `"}`, "", "body_too_large", false},
		{"hard_cap", strings.Repeat(" ", moderationImageBodyLimit+1), "", "body_too_large", false},
		{"duplicate_key", strings.Replace(mixed("hello", image), `"model":`, `"model":"other","model":`, 1), "", "invalid_json", false},
		{"malformed", base + strings.Repeat(" ", moderationTextBodyLimit) + `{`, "", "invalid_json", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/v1/moderations", strings.NewReader(tt.body))
			// Also covers chunked requests: trust bytes read, not Content-Length.
			r.ContentLength = -1
			in, fallback, err := readModerationRequest(httptest.NewRecorder(), r)
			var text string
			if err == nil {
				text, err = parseModerationText(in.Input, fallback)
			}
			if tt.wantCode != "" {
				if err == nil || errorCode(err) != tt.wantCode {
					t.Fatalf("wanted %s, got %v", tt.wantCode, err)
				}
				return
			}
			if err != nil || fallback != tt.wantFallback || text != tt.wantText || in.Model != "abuse-audit-v1" {
				t.Fatalf("unexpected result: fallback=%v, text length=%d, error=%v", fallback, len(text), err)
			}
		})
	}
}

func TestTextFallbackRejectsUnsupportedAndMalformedBlocks(t *testing.T) {
	for _, raw := range []string{
		`[{"type":"image_url","image_url":{"url":"data:image/png;base64,abc"}}]`,
		`[{"type":"text","text":"hello"},{"type":"audio","data":"abc"}]`,
		`[{"type":"text","text":"hello"},{"type":"image_url"}]`,
		`[{"type":"text","text":"hello"},{"type":"image_url","image_url":{"url":"a"},"text":"do not discard"}]`,
		`[{"type":"text","text":"hello","extra":"ignored?"}]`,
		`[{"type":"text","text":null}]`,
	} {
		if _, err := parseModerationText(json.RawMessage(raw), true); err == nil {
			t.Fatalf("accepted unsupported input: %s", raw)
		}
	}
}

func TestAdminJSONBodyLimitUnchanged(t *testing.T) {
	r := httptest.NewRequest("POST", "/admin/example", strings.NewReader(strings.Repeat(" ", moderationTextBodyLimit+1)))
	var payload any
	if err := readJSON(httptest.NewRecorder(), r, &payload); err == nil || errorCode(err) != "body_too_large" {
		t.Fatalf("admin request body limit changed: %v", err)
	}
}
