package audit

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestRequestAdmissionBudgets(t *testing.T) {
	c := DefaultRuntimeConfig()
	c.RequestConcurrency = 2
	c.RequestBodyMiB = 32
	a := requestAdmission{config: c}
	release, err := a.acquire(-1, 32<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.acquire(1, 32<<20); errorCode(err) != "request_capacity_exceeded" {
		t.Fatal(err)
	}
	release()
	release()
	x, err := a.acquire(1, 32<<20)
	if err != nil {
		t.Fatal(err)
	}
	y, err := a.acquire(1, 32<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.acquire(1, 32<<20); errorCode(err) != "request_capacity_exceeded" {
		t.Fatal(err)
	}
	x()
	y()
	if a.active != 0 || a.bytes != 0 {
		t.Fatal("admission leaked")
	}
	if _, err := a.acquire(33<<20, 32<<20); errorCode(err) != "body_too_large" {
		t.Fatal(err)
	}
}
func TestRuntimeEnvironmentValidation(t *testing.T) {
	t.Setenv("AUDIT_MODEL_CONCURRENCY", "8")
	t.Setenv("AUDIT_TRIAL_CONCURRENCY", "2")
	c, err := RuntimeConfigFromEnv()
	if err != nil || c.ModelConcurrency != 8 || c.TrialConcurrency != 2 {
		t.Fatal(c, err)
	}
	t.Setenv("AUDIT_REQUEST_BODY_MIB", "invalid")
	if _, err := RuntimeConfigFromEnv(); err == nil {
		t.Fatal("invalid environment accepted")
	}
}
func TestStrictImageLimits(t *testing.T) {
	for _, url := range []string{"data:image/png;base64,invalid!", "data:image/svg+xml;base64,PHN2Zz4=", "data:image/png;base64,YQ"} {
		if validAuditImage(AuditImage{URL: url}) {
			t.Fatal("invalid embedded image accepted", url)
		}
	}
	if !validAuditImage(AuditImage{URL: "data:image/png;base64,YQ=="}) {
		t.Fatal("valid Base64 rejected")
	}
	block := `{"type":"image_url","image_url":{"url":"https://example.com/image.png"}}`
	if _, _, err := parseModerationInput(json.RawMessage(fmt.Sprintf("[%s,%s]", block, block)), 1); errorCode(err) != "too_many_images" {
		t.Fatal(err)
	}
}
func TestTrialQuotaDoesNotOccupyProductionSlots(t *testing.T) {
	s := testStore(t)
	p, key := configureBillingPolicy(t, s, 0)
	app, err := NewServer(s, "http://localhost:8090", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < cap(app.trialSlots); i++ {
		app.trialSlots <- struct{}{}
	}
	_, err = runTestAudit(t, app, p, "admin", "test", "quota")
	if errorCode(err) != "trial_capacity_exceeded" {
		t.Fatal(err)
	}
	if len(app.Engine.slots) != 0 {
		t.Fatal("trial quota consumed upstream slots")
	}
	app.Engine.Client = &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(cachedTestResponse))}, nil
	})}
	if result, err := runTestAudit(t, app, p, key.ID, "production", "production quota"); err != nil || len(result.Results) != 1 {
		t.Fatal("trial quota blocked production", err)
	}
	for i := 0; i < cap(app.trialSlots); i++ {
		<-app.trialSlots
	}
}
