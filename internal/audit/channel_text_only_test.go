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

func TestTextOnlyChannelRouting(t *testing.T) {
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
	unchecked := createTestChannel(t, store, cred, "unchecked", "deepseek-unchecked")
	primary := createTestChannel(t, store, cred, "primary", "deepseek-flash")
	backup := createTestChannel(t, store, cred, "backup", "deepseek-v4-pro")
	// Exercise the same JSON handler used by the channel editor.
	saveTextOnly := func(c ModelChannel, enabled bool) ModelChannel {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"name": c.Name, "model": c.Model, "credential_id": c.CredentialID, "timeout_ms": c.TimeoutMS, "max_tokens": c.MaxTokens, "max_concurrency": c.MaxConcurrency, "enabled": c.Enabled, "text_only": enabled, "expected_revision": c.Revision})
		r := httptest.NewRequest("PUT", "/admin/model-channels/"+c.ID, strings.NewReader(string(raw)))
		r.SetPathValue("id", c.ID)
		r = r.WithContext(context.WithValue(ctx, sessionKey{}, session{Username: "admin"}))
		w := httptest.NewRecorder()
		if err := app.saveChannel(w, r); err != nil {
			t.Fatal(err)
		}
		var saved ModelChannel
		if json.Unmarshal(w.Body.Bytes(), &saved) != nil || saved.TextOnly != enabled || saved.Revision != c.Revision+1 {
			t.Fatalf("channel editor lost text_only: %s", w.Body.String())
		}
		return saved
	}
	primary = saveTextOnly(primary, true)
	backup = saveTextOnly(backup, true)
	p, err := store.Policy(ctx, "abuse-default")
	if err != nil {
		t.Fatal(err)
	}
	p.Config.StoreInput = true
	p.Config.ResultCacheTTL = 60
	p.Config.Channels = []ChannelBinding{{unchecked.ID, 3, 100, false}, {primary.ID, 1, 100, true}, {backup.ID, 2, 100, true}}
	if err := store.SaveConfig(ctx, "admin", p.ID, p.Name, p.Revision, p.Config); err != nil {
		t.Fatal(err)
	}
	p, _ = store.Policy(ctx, p.ID)
	if err := store.SetPolicyState(ctx, "admin", p.ID, p.Revision, true); err != nil {
		t.Fatal(err)
	}
	key, err := store.CreateKey(ctx, "admin", "text-only-test", []string{p.ID}, 10000)
	if err != nil {
		t.Fatal(err)
	}

	handler := app.Handler()
	for _, tt := range []struct {
		name, image, text, wantCalls, scope  string
		primaryText, backupText, failPrimary bool
		status                               int
	}{
		{"text_small", "https://example.com/private-image", "inspect screenshot", primary.Model, "text_only", true, true, false, 200},
		{"text_large", "data:image/png;base64,private-image" + strings.Repeat("a", 1<<20), "inspect screenshot", primary.Model, "text_only", true, true, false, 200},
		{"multimodal", "https://example.com/private-image", "inspect screenshot", primary.Model, "text_and_images", false, true, false, 200},
		{"multimodal_large", "data:image/png;base64,private-image" + strings.Repeat("a", 1<<20), "inspect screenshot", primary.Model, "text_and_images", false, true, false, 200},
		{"image_only", "https://example.com/private-image", "", backup.Model, "text_and_images", true, false, false, 200},
		{"no_text", "https://example.com/private-image", "", "", "", true, true, false, 400},
		{"text_to_image", "https://example.com/private-image", "inspect screenshot", primary.Model + "," + backup.Model, "text_and_images", true, false, true, 200},
		{"image_to_text", "https://example.com/private-image", "inspect screenshot", primary.Model + "," + backup.Model, "text_only", false, true, true, 200},
		{"plain_text", "", "inspect screenshot", primary.Model, "text", false, false, false, 200},
		{"cache_images", "data:image/png;base64,cHJpdmF0ZS1pbWFnZQ==", "inspect screenshot", primary.Model, "text_and_images", false, false, false, 200},
		{"cache_text", "data:image/png;base64,cHJpdmF0ZS1pbWFnZQ==", "inspect screenshot", primary.Model, "text_only", true, true, false, 200},
	} {
		t.Run(tt.name, func(t *testing.T) {
			primary = saveTextOnly(primary, tt.primaryText)
			backup = saveTextOnly(backup, tt.backupText)
			var called []string
			currentImage := tt.image
			app.Engine.Client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
				raw, _ := io.ReadAll(r.Body)
				var input struct {
					Model    string
					Messages []struct {
						Role    string
						Content json.RawMessage
					}
				}
				if json.Unmarshal(raw, &input) != nil || len(input.Messages) != 2 {
					t.Fatal("invalid chat payload")
				}
				textOnly := tt.primaryText
				if input.Model == backup.Model {
					textOnly = tt.backupText
				}
				if textOnly || currentImage == "" {
					var content string
					if json.Unmarshal(input.Messages[1].Content, &content) != nil || content != "<user_input>"+tt.text+"</user_input>" {
						t.Fatal("text channel received images or wrong text")
					}
				} else {
					var parts []struct {
						Type, Text string
						ImageURL   AuditImage `json:"image_url"`
					}
					if json.Unmarshal(input.Messages[1].Content, &parts) != nil || len(parts) != 2 || parts[0].Text != "<user_input>"+tt.text+"</user_input>" || parts[1].Type != "image_url" || parts[1].ImageURL.URL != currentImage {
						t.Fatal("multimodal channel lost text or image")
					}
				}
				called = append(called, input.Model)
				if tt.failPrimary && input.Model == primary.Model {
					return &http.Response{StatusCode: 503, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
				}
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"choices":[{"finish_reason":"stop","message":{"content":"{\"confidence\":0.9,\"reason\":\"测试命中\"}"}}]}`))}, nil
			})}
			call := func() *httptest.ResponseRecorder {
				t.Helper()
				var input any = tt.text
				if currentImage != "" {
					input = []any{map[string]any{"type": "text", "text": tt.text}, map[string]any{"type": "image_url", "image_url": map[string]string{"url": currentImage}}}
				}
				raw, _ := json.Marshal(map[string]any{"model": p.Alias, "input": input})
				r := httptest.NewRequest("POST", "/v1/moderations", strings.NewReader(string(raw)))
				r.Header.Set("Authorization", "Bearer "+key)
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				if w.Code != tt.status {
					t.Fatalf("status=%d, response=%s", w.Code, w.Body.String())
				}
				return w
			}
			w := call()
			if strings.Join(called, ",") != tt.wantCalls {
				t.Fatalf("wrong channel sequence: %v", called)
			}
			log, err := store.LogDetail(ctx, w.Header().Get("X-Audit-Request-ID"))
			if err != nil {
				t.Fatal(err)
			}
			if log.Request.InputScope != tt.scope || log.Request.TextOnlyFallback != (tt.scope == "text_only") {
				t.Fatalf("wrong final scope: %+v", log.Request)
			}
			if tt.status == 200 {
				if log.Input != tt.text || !log.Flagged || w.Header().Get("X-Audit-Input-Scope") != strings.ReplaceAll(tt.scope, "_", "-") {
					t.Fatal("lost input/verdict/scope")
				}
				for _, a := range log.Attempts {
					textOnly := tt.primaryText
					if a.ChannelID == backup.ID {
						textOnly = tt.backupText
					}
					var imgs []AuditImage
					if tt.image != "" {
						imgs = []AuditImage{{URL: tt.image}}
					}
					if a.InputScope != auditInputScope(textOnly, imgs) || (a.ImageCount > 0) != (len(imgs) > 0 && !textOnly) {
						t.Fatal("wrong attempt scope")
					}
				}
			} else if log.ErrorCode != "empty_input" {
				t.Fatal("image-only text channels should fail with empty_input")
			}
			if strings.HasPrefix(tt.name, "cache_") {
				call()
				if len(called) != 1 {
					t.Fatal("identical effective input missed cache")
				}
				currentImage = "data:image/png;base64,ZGlmZmVyZW50LWltYWdl"
				call()
				want := 2
				if tt.primaryText {
					want = 1
				}
				if len(called) != want {
					t.Fatal("cache did not distinguish images according to channel mode")
				}
			}
			if tt.name == "multimodal" {
				call()
				if len(called) != 2 {
					t.Fatal("mutable remote image URL reused stale cache")
				}
			}
			var metadata string
			if err := store.DB.QueryRowContext(ctx, "SELECT metadata::text FROM audit_requests WHERE id=$1", log.ID).Scan(&metadata); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(metadata, "private-image") || strings.Contains(metadata, "data:image") {
				t.Fatal("image data persisted in audit metadata")
			}
		})
	}
}

func TestTextOnlyChannelMigration(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	cred, err := store.SaveCredential(ctx, "admin", "", "test", "test-secret", true)
	if err != nil {
		t.Fatal(err)
	}
	c := createTestChannel(t, store, cred, "legacy", "deepseek-flash")
	if _, err := store.DB.ExecContext(ctx, "ALTER TABLE audit_model_channels DROP COLUMN text_only"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := store.DB.ExecContext(ctx, schema); err != nil {
			t.Fatal(err)
		}
	}
	channels, err := store.Channels(ctx)
	if err != nil || len(channels) != 1 || channels[0].ID != c.ID || channels[0].TextOnly {
		t.Fatalf("migration changed existing channel: %+v, %v", channels, err)
	}
}
