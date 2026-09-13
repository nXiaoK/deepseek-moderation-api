package audit

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestModerationRequestRecordsIncludeRejections(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	app, err := NewServer(store, "http://localhost:8090", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cred, err := store.SaveCredential(ctx, "admin", "", "test", "test-secret", true)
	if err != nil {
		t.Fatal(err)
	}
	channel := createTestChannel(t, store, cred, "ingress", "deepseek-flash")
	p, err := store.Policy(ctx, "abuse-default")
	if err != nil {
		t.Fatal(err)
	}
	p.Config.Channels = []ChannelBinding{{channel.ID, 1, 100, true}}
	p.Config.StoreInput = true
	if err = store.SaveConfig(ctx, "admin", p.ID, p.Name, p.Revision, p.Config); err != nil {
		t.Fatal(err)
	}
	p, _ = store.Policy(ctx, p.ID)
	if err = store.SetPolicyState(ctx, "admin", p.ID, p.Revision, true); err != nil {
		t.Fatal(err)
	}
	key, err := store.CreateKey(ctx, "admin", "sub2api", []string{p.ID}, 10000)
	if err != nil {
		t.Fatal(err)
	}
	forbidden, err := store.CreateKey(ctx, "admin", "forbidden", []string{}, 10000)
	if err != nil {
		t.Fatal(err)
	}
	limited, err := store.CreateKey(ctx, "admin", "limited", []string{p.ID}, 1)
	if err != nil {
		t.Fatal(err)
	}
	limitedKey, err := store.AuthenticateKey(ctx, limited)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Rate(ctx, "api:"+limitedKey.ID, 1); err != nil {
		t.Fatal(err)
	}
	modelCalls := 0
	var modelInput string
	modelOutput := `{"confidence":0.9,"reason":"测试命中"}`
	app.Engine.Client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		modelCalls++
		input, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		modelInput = string(input)
		raw, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"content": modelOutput}}}})
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
	})}
	handler := app.Handler()
	valid := `{"model":"abuse-audit-v1","input":"test input"}`
	cases := []struct {
		name, body, key, stage string
		status                 int
		cancelled              bool
	}{
		{"missing_key", valid, "", "authentication", 401, false},
		{"invalid_key", valid, "invalid-secret", "authentication", 401, false},
		{"cancelled", valid, "", "authentication", 401, true},
		{"rate_limited", valid, limited, "rate_limit", 429, false},
		{"bad_json", `{`, key, "request_validation", 400, false},
		{"oversized", `{"model":"abuse-audit-v1","input":"` + strings.Repeat("x", 1024*1024+1) + `"}`, key, "request_validation", 413, false},
		{"unknown_policy", `{"model":"missing","input":"test"}`, key, "policy_check", 404, false},
		{"forbidden", valid, forbidden, "policy_check", 403, false},
		{"image", `{"model":"abuse-audit-v1","input":[{"type":"text","text":"inspect screenshot"},{"type":"image_url","image_url":{"url":"data:image/png;base64,cHJpdmF0ZS1pbWFnZQ=="}}]}`, key, "completed", 200, false},
		{"empty", `{"model":"abuse-audit-v1","input":""}`, key, "input_validation", 400, false},
		{"success", valid, key, "completed", 200, false},
		{"image_fallback", `{"model":"abuse-audit-v1","input":[{"type":"text","text":"inspect screenshot"},{"type":"image_url","image_url":{"url":"data:image/png;base64,cHJpdmF0ZS1pbWFnZQAA` + strings.Repeat("a", 1024*1024) + `"}}]}`, key, "completed", 200, false},
		{"model_failure", valid, key, "audit", 502, false},
		{"input_storage_off", `{"model":"abuse-audit-v1","input":[{"type":"text","text":"inspect screenshot"},{"type":"image_url","image_url":{"url":"data:image/png;base64,cHJpdmF0ZS1pbWFnZQ=="}}]}`, key, "completed", 200, false},
		{"disabled_policy", valid, key, "policy_check", 503, false},
		{"revoked_key", valid, key, "authentication", 401, false},
	}
	for index, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if tt.name == "image_fallback" || tt.name == "input_storage_off" {
				channel.TextOnly = tt.name == "image_fallback"
				channel, err = store.SaveChannel(ctx, "admin", channel)
				if err != nil {
					t.Fatal(err)
				}
			}
			if tt.name == "model_failure" {
				modelOutput = `{"confidence":null}`
			}
			if tt.name == "input_storage_off" {
				modelOutput = `{"confidence":0.9,"reason":"测试命中"}`
				p, _ = store.Policy(ctx, p.ID)
				p.Config.StoreInput = false
				if err := store.SaveConfig(ctx, "admin", p.ID, p.Name, p.Revision, p.Config); err != nil {
					t.Fatal(err)
				}
			}
			if tt.name == "disabled_policy" {
				p, _ = store.Policy(ctx, p.ID)
				if err := store.SetPolicyState(ctx, "admin", p.ID, p.Revision, false); err != nil {
					t.Fatal(err)
				}
			}
			if tt.name == "revoked_key" {
				client, err := store.AuthenticateKey(ctx, key)
				if err != nil {
					t.Fatal(err)
				}
				if err = store.RevokeKey(ctx, "admin", client.ID); err != nil {
					t.Fatal(err)
				}
			}
			r := httptest.NewRequest("POST", "/v1/moderations", strings.NewReader(tt.body))
			if tt.key != "" {
				r.Header.Set("Authorization", "Bearer "+tt.key)
			}
			if tt.cancelled {
				cancelled, cancel := context.WithCancel(r.Context())
				cancel()
				r = r.WithContext(cancelled)
			}
			w := httptest.NewRecorder()
			before := modelCalls
			handler.ServeHTTP(w, r)
			if w.Code != tt.status {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			id := w.Header().Get("X-Audit-Request-ID")
			if id == "" {
				t.Fatal("missing correlation ID")
			}
			log, err := store.LogDetail(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if log.Request == nil || log.Request.Stage != tt.stage || log.Request.HTTPStatus != tt.status {
				t.Fatalf("request metadata: %+v", log.Request)
			}
			if log.Kind != "production" {
				t.Fatal("missing production request")
			}
			if tt.status != 200 && (log.ErrorCode == "" || log.ErrorMessage == "" || log.Confidence != nil || log.Flagged) {
				t.Fatalf("incorrect failure verdict: %+v", log)
			}
			if tt.stage != "audit" && tt.stage != "completed" && (modelCalls != before || log.AttemptCount != 0) {
				t.Fatal("early rejection called model")
			}
			if tt.name == "success" {
				var response Response
				if json.Unmarshal(w.Body.Bytes(), &response) != nil || response.ID != id || !log.Flagged || log.Confidence == nil {
					t.Fatal("success lost verdict or duplicated ID")
				}
			}
			if tt.name == "image" && (log.Request.ImageCount != 1 || log.Request.TextChars != 18 || log.Input != "inspect screenshot") {
				t.Fatalf("image metadata/input: %+v", log)
			}
			if tt.name == "image" && (log.Request.InputScope != "text_and_images" || !strings.Contains(modelInput, "data:image/png;base64,cHJpdmF0ZS1pbWFnZQ==")) {
				t.Fatal("image was not forwarded")
			}
			if tt.name == "image_fallback" {
				if !log.Request.TextOnlyFallback || log.Request.ImageCount != 1 || log.Input != "inspect screenshot" || log.Confidence == nil || !log.Flagged || modelCalls != before+1 || w.Header().Get("X-Audit-Input-Scope") != "text-only" {
					t.Fatalf("fallback lost verdict or scope: %+v", log)
				}
				if !strings.Contains(modelInput, "inspect screenshot") || strings.Contains(modelInput, "private-image") || strings.Contains(modelInput, "data:image") {
					t.Fatal("model did not receive only the extracted text")
				}
			}
			if tt.name == "input_storage_off" {
				var cipher []byte
				if err := store.DB.QueryRow("SELECT input_cipher FROM audit_requests WHERE id=$1", id).Scan(&cipher); err != nil {
					t.Fatal(err)
				}
				if log.InputStored || log.Input != "" || len(cipher) != 0 {
					t.Fatal("ignored disabled input retention")
				}
			}
			var raw string
			if err = store.DB.QueryRow("SELECT metadata::text FROM audit_requests WHERE id=$1", id).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(raw, "private-image") || strings.Contains(raw, "inspect screenshot") || strings.Contains(raw, "invalid-secret") {
				t.Fatal("sensitive request data leaked into metadata")
			}
			_, total, err := store.Logs(ctx, LogFilter{Page: 1, PageSize: 100})
			if err != nil || total != index+1 {
				t.Fatalf("expected exactly one record per request, got %d: %v", total, err)
			}
		})
	}
}
