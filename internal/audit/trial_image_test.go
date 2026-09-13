package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestImageTrialUsesProductionParserAndKeepsScope(t *testing.T) {
	s := testStore(t)
	p, _ := configureBillingPolicy(t, s, 0)
	app, err := NewServer(s, "http://localhost:8090", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var payload string
	app.Engine.Client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(r.Body)
		payload = string(raw)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(cachedTestResponse))}, nil
	})}
	input := json.RawMessage(`[{"type":"image_url","image_url":{"url":"data:image/png;base64,YQ==","detail":"high"}}]`)
	raw, _ := json.Marshal(map[string]any{"input": input})
	r := httptest.NewRequest("POST", "/admin/policies/"+p.ID+"/test", bytes.NewReader(raw))
	r.SetPathValue("id", p.ID)
	r = r.WithContext(context.WithValue(r.Context(), sessionKey{}, session{Username: "admin"}))
	w := httptest.NewRecorder()
	if err := app.testPolicy(w, r); err != nil {
		t.Fatal(err)
	}
	var result Response
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.InputScope != "text_and_images" || !strings.Contains(payload, "data:image/png;base64,YQ==") {
		t.Fatal("image not forwarded", result.InputScope)
	}
	log, err := s.LogDetail(context.Background(), result.ID)
	if err != nil || log.Request == nil || log.Request.ImageCount != 1 || log.Request.InputScope != "text_and_images" {
		t.Fatal(log.Request, err)
	}
	bad, _ := json.Marshal(map[string]any{"input": json.RawMessage(`[{"type":"image_url","image_url":{"url":"data:image/png;base64,invalid"}}]`)})
	r = httptest.NewRequest("POST", "/test", bytes.NewReader(bad))
	if err := app.testPolicy(httptest.NewRecorder(), r); errorCode(err) != "invalid_input" {
		t.Fatal("invalid image accepted", err)
	}
}
