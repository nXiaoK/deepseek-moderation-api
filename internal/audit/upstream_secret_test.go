package audit

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUpstreamErrorsRedactActualCredential(t *testing.T) {
	t.Setenv("AUDIT_SUB2API_ORIGINS", "https://gateway.example")
	for _, provider := range []string{ProviderDeepSeek, ProviderGrok} {
		for _, credential := range []string{"xai-fake-review-credential", "sk_fake_gateway_key", "arbitrary/credential+123="} {
			for _, field := range []string{"detail", "message"} {
				t.Run(provider+"/"+credential+"/"+field, func(t *testing.T) {
					cfg := DefaultConfig()
					cfg.Provider, cfg.BaseURL = provider, "https://gateway.example"
					message := "Forbidden API key: " + credential + " (key=" + credential + ")"
					payload := map[string]any{"detail": message}
					if field == "message" {
						payload = map[string]any{"error": map[string]string{"message": message}}
					}
					raw, _ := json.Marshal(payload)
					engine := NewEngine(1)
					engine.Client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
						if r.Header.Get("Authorization") != "Bearer "+credential {
							t.Fatal("test did not use the expected credential")
						}
						return &http.Response{StatusCode: 403, Body: io.NopCloser(strings.NewReader(string(raw))), Header: http.Header{}}, nil
					})}
					_, usage, _, err := engine.Assess(context.Background(), cfg, credential, "test")
					var failure *upstreamFailure
					if !errors.As(err, &failure) || failure.Code != "upstream_auth_failed" || !usage.Attempted {
						t.Fatalf("unexpected failure: %v, usage=%+v", err, usage)
					}
					if strings.Contains(err.Error(), credential) || strings.Contains(storedAuditError(err), credential) {
						t.Fatal("credential survived error sanitization")
					}
					if strings.Count(failure.Message, "[隐去]") != 2 {
						t.Fatalf("expected both occurrences redacted: %q", failure.Message)
					}
					w := httptest.NewRecorder()
					(&Server{}).wrap(func(http.ResponseWriter, *http.Request) error { return err })(w, httptest.NewRequest("POST", "/v1/moderations", nil))
					if w.Code != 503 || strings.Contains(w.Body.String(), credential) {
						t.Fatalf("unsafe API error: %d %s", w.Code, w.Body.String())
					}
				})
			}
		}
	}
}
