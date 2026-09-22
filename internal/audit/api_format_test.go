package audit

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestSelectedAPIFormatControlsURLAndPayload(t *testing.T) {
	t.Setenv("AUDIT_SUB2API_ORIGINS", "https://api.cline.bot")
	for _, provider := range []string{ProviderDeepSeek, ProviderGrok} {
		for _, format := range []string{APIFormatResponses, APIFormatChatCompletions} {
			for _, path := range []string{"", "/", "/api/v1", "/api/v1/", "/v1", "/custom%2Fprefix"} {
				t.Run(provider+"/"+format+path, func(t *testing.T) {
					cfg := DefaultConfig()
					cfg.Provider, cfg.APIFormat, cfg.BaseURL = provider, format, "https://api.cline.bot"+path
					prefix := strings.TrimRight(path, "/")
					if prefix == "" {
						prefix = "/v1"
					}
					suffix := "/chat/completions"
					if format == APIFormatResponses {
						suffix = "/responses"
					}
					engine := NewEngine(1)
					calls := 0
					engine.Client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
						calls++
						if r.URL.String() != "https://api.cline.bot"+prefix+suffix || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer test-api-key" {
							t.Fatal("wrong request target or credentials", r.Method, r.URL)
						}
						var payload map[string]any
						if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
							t.Fatal(err)
						}
						if format == APIFormatResponses {
							if payload["input"] == nil || payload["messages"] != nil || payload["stream"] != true || payload["max_output_tokens"] != float64(cfg.MaxTokens) {
								t.Fatal("wrong Responses payload", payload)
							}
							if provider == ProviderDeepSeek && payload["reasoning"] != nil {
								t.Fatal("Grok-specific parameter sent to generic Responses endpoint")
							}
							return grokStreamResponse(grokSSE(`{"confidence":0.2,"reason":"正常"}`, "")), nil
						}
						if payload["messages"] == nil || payload["input"] != nil || payload["stream"] != false || payload["max_tokens"] != float64(cfg.MaxTokens) || payload["thinking"] != nil {
							t.Fatal("wrong Chat Completions payload", payload)
						}
						return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"choices":[{"finish_reason":"stop","message":{"content":"{\"confidence\":0.2,\"reason\":\"正常\"}"}}]}`))}, nil
					})}
					result, _, _, err := engine.Assess(context.Background(), cfg, "test-api-key", "hello", AuditImage{URL: "https://example.test/image.png"})
					if err != nil || calls != 1 || result.Confidence != .2 {
						t.Fatal(result, calls, err)
					}
				})
			}
		}
	}
	cfg := DefaultConfig()
	cfg.APIFormat = APIFormatChatCompletions
	if cfg.inferenceURL() != "https://api.deepseek.com/chat/completions" {
		t.Fatal("official DeepSeek default changed", cfg.inferenceURL())
	}
	cfg.APIFormat = "invalid"
	if cfg.Validate() == nil {
		t.Fatal("invalid format accepted")
	}
}

func TestAPIFormatCredentialPersistenceAndBinding(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	const base = "https://api.cline.bot/api/v1"
	id, err := store.SaveProviderCredential(ctx, "admin", "", "cline", "test-api-key", true, ProviderDeepSeek, base, APIFormatResponses)
	if err != nil {
		t.Fatal(err)
	}
	channel := createTestChannel(t, store, id, "cline", "test-model")
	for _, format := range []string{APIFormatResponses, APIFormatChatCompletions} {
		if _, err := store.SaveProviderCredential(ctx, "admin", id, "cline", "", true, ProviderDeepSeek, base, format); err != nil {
			t.Fatal(err)
		}
		// Older clients omitting the new field must not silently reset it.
		if _, err := store.SaveProviderCredential(ctx, "admin", id, "cline", "replacement-test-key", true, ProviderDeepSeek, base); err != nil {
			t.Fatal(err)
		}
		items, err := store.Credentials(ctx)
		if err != nil || len(items) != 1 || items[0].APIFormat != format || items[0].BaseURL != base {
			t.Fatal("connection format not persisted", items, err)
		}
		channels, err := store.Channels(ctx)
		if err != nil || len(channels) != 1 || channels[0].APIFormat != format || channels[0].CacheEpoch == channel.CacheEpoch {
			t.Fatal("channel format or cache epoch stale", channels, err)
		}
		channel = channels[0]
		cfg := channel.Inference(DefaultSettings())
		if key, err := store.CredentialForConfig(ctx, cfg); err != nil || key != "replacement-test-key" {
			t.Fatal("updated binding unusable", err)
		}
		cfg.APIFormat = ""
		if _, err := store.CredentialForConfig(ctx, cfg); errorCode(err) != "credential_provider_mismatch" {
			t.Fatal("stale format accepted", err)
		}
	}
	if _, err := store.SaveProviderCredential(ctx, "admin", id, "cline", "", true, ProviderDeepSeek, base, "invalid"); errorCode(err) != "invalid_connection" {
		t.Fatal("invalid format accepted", err)
	}
}

func TestGrokModelDiscoveryUsesSelectedBasePath(t *testing.T) {
	t.Setenv("AUDIT_SUB2API_ORIGINS", "https://api.cline.bot")
	engine := NewEngine(1)
	engine.Client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://api.cline.bot/api/v1/models" || r.Method != "GET" {
			t.Fatal("wrong model discovery path", r.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":[]}`))}, nil
	})}
	for _, format := range []string{APIFormatResponses, APIFormatChatCompletions} {
		cfg := grokTestConfig()
		cfg.BaseURL, cfg.APIFormat = "https://api.cline.bot/api/v1/", format
		if _, err := engine.ProbeGrok(context.Background(), cfg, "test-api-key"); err != nil {
			t.Fatal(err)
		}
	}
}
