package audit

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestMultimodalProviderPayloads(t *testing.T) {
	t.Setenv("AUDIT_SUB2API_ORIGINS", "https://sub2api.test")
	images := []AuditImage{{URL: "data:image/png;base64,aW1hZ2U=", Detail: "high"}, {URL: "https://example.com/second.png", Detail: "low"}}
	for _, provider := range []string{ProviderDeepSeek, ProviderGrok} {
		for _, textOnly := range []bool{false, true} {
			for _, text := range []string{"inspect screenshot", ""} {
				t.Run(provider+auditInputScope(textOnly, images)+text, func(t *testing.T) {
					cfg := DefaultConfig()
					if provider == ProviderGrok {
						cfg = grokTestConfig()
					}
					cfg.TextOnly = textOnly
					engine := NewEngine(1)
					calls := 0
					engine.Client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
						calls++
						var payload map[string]json.RawMessage
						if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
							t.Fatal(err)
						}
						var content json.RawMessage
						if provider == ProviderDeepSeek {
							var messages []struct {
								Role    string
								Content json.RawMessage
							}
							if json.Unmarshal(payload["messages"], &messages) != nil || len(messages) != 2 || messages[1].Role != "user" {
								t.Fatal("wrong chat messages")
							}
							content = messages[1].Content
						} else if textOnly {
							content = payload["input"]
						} else {
							var messages []struct {
								Role    string
								Content json.RawMessage
							}
							if json.Unmarshal(payload["input"], &messages) != nil || len(messages) != 1 || messages[0].Role != "user" {
								t.Fatal("wrong responses input")
							}
							content = messages[0].Content
						}
						if textOnly {
							var got string
							if json.Unmarshal(content, &got) != nil || got != "<user_input>"+text+"</user_input>" {
								t.Fatal("text-only model received non-text input")
							}
						} else {
							var parts []map[string]json.RawMessage
							if json.Unmarshal(content, &parts) != nil || len(parts) != 3 {
								t.Fatal("multimodal input lost images")
							}
							var gotText string
							_ = json.Unmarshal(parts[0]["text"], &gotText)
							if gotText != "<user_input>"+text+"</user_input>" {
								t.Fatal("multimodal text changed")
							}
							for i, want := range images {
								var typ string
								_ = json.Unmarshal(parts[i+1]["type"], &typ)
								var got AuditImage
								if provider == ProviderDeepSeek {
									if typ != "image_url" {
										t.Fatal("wrong chat image type")
									}
									_ = json.Unmarshal(parts[i+1]["image_url"], &got)
								} else {
									if typ != "input_image" {
										t.Fatal("wrong responses image type")
									}
									_ = json.Unmarshal(parts[i+1]["image_url"], &got.URL)
									_ = json.Unmarshal(parts[i+1]["detail"], &got.Detail)
								}
								if got != want {
									t.Fatal("image URL/detail/order changed")
								}
							}
						}
						if provider == ProviderGrok {
							return grokStreamResponse(grokSSE(`{"confidence":0.9,"reason":"test"}`, "")), nil
						}
						return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"choices":[{"finish_reason":"stop","message":{"content":"{\"confidence\":0.9,\"reason\":\"test\"}"}}]}`))}, nil
					})}
					_, _, _, err := engine.Assess(context.Background(), cfg, "test-key", text, images...)
					if textOnly && text == "" {
						if err == nil || errorCode(err) != "empty_input" || calls != 0 {
							t.Fatal("empty text-only request reached model")
						}
					} else if err != nil || calls != 1 {
						t.Fatalf("model call failed: %v, calls=%d", err, calls)
					}
				})
			}
		}
	}
}

func TestMultimodalInputValidation(t *testing.T) {
	for _, raw := range []string{
		`[{"type":"image_url","image_url":{"url":"file:///tmp/image"}}]`,
		`[{"type":"image_url","image_url":{"url":"https://user:pass@example.com/image"}}]`,
		`[{"type":"image_url","image_url":{"url":"data:image/png;base64,"}}]`,
		`[{"type":"image_url","image_url":{"url":"https://example.com/image","detail":"invalid"}}]`,
		`[{"type":"image_url","image_url":{"url":"https://example.com/image"},"text":"hidden text"}]`,
	} {
		if _, _, err := parseModerationInput(json.RawMessage(raw)); err == nil {
			t.Fatal("accepted invalid image input", raw)
		}
	}
	text, images, err := parseModerationInput(json.RawMessage(`[{"type":"image_url","image_url":{"url":"https://example.com/image"}}]`))
	if err != nil || text != "" || len(images) != 1 {
		t.Fatal("image-only input rejected", err)
	}
}

func TestMultimodalBudgetCannotUseTextEstimate(t *testing.T) {
	store := testStore(t)
	p, key := configureBillingPolicy(t, store, 0)
	ctx := context.Background()
	cfg := testInference(t, store, p)
	cfg.ImageCount = 1
	if _, err := store.DB.ExecContext(ctx, "INSERT INTO client_budgets(client_id,daily_limit) VALUES($1,1000000000000)", key.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReserveCost(ctx, randomToken("cost_"), key.ID, "production", p, cfg, "text", false, time.Now()); err == nil || errorCode(err) != "pricing_unavailable" {
		t.Fatal("image request used text-only budget estimate", err)
	}
}
