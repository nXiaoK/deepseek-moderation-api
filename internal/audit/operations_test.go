package audit

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

func TestOperationsReportAlertsAndBoundaries(t *testing.T) {
	app, p, call := evaluationTestServer(t)
	s := app.Store
	channels, err := s.Channels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var report OperationsReport
	if err := json.Unmarshal(call("GET", "/admin/operations", nil, 200), &report); err != nil {
		t.Fatal(err)
	}
	if report.MigrationVersion < 1 || report.Runtime.ModelConcurrency != 16 {
		t.Fatal(report)
	}
	found := false
	for _, a := range report.Alerts {
		if a.ID == "unverified:"+channels[0].ID {
			found = true
		}
	}
	if !found {
		t.Fatal("unverified channel called healthy")
	}
	if _, err := s.DB.Exec("UPDATE audit_model_channels SET enabled=FALSE WHERE id=$1", channels[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(call("GET", "/admin/operations", nil, 200), &report); err != nil {
		t.Fatal(err)
	}
	found = false
	for _, a := range report.Alerts {
		if a.ID == "policy:"+p.ID && a.Severity == "critical" {
			found = true
		}
	}
	if !found {
		t.Fatal("unavailable policy not alerted", report.Alerts)
	}
	app.notePersistenceFailure("test-request", "test")
	if err := json.Unmarshal(call("GET", "/admin/operations", nil, 200), &report); err != nil {
		t.Fatal(err)
	}
	found = false
	for _, a := range report.Alerts {
		if a.ID == "persistence" {
			found = true
		}
	}
	if !found || report.PersistenceFailures != 1 {
		t.Fatal("persistence failure was not surfaced")
	}
	w := httptest.NewRecorder()
	app.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/admin/operations", nil))
	if w.Code != 401 {
		t.Fatal("operations exposed without login")
	}
}
func TestCancellationCountsWithoutPoisoningChannel(t *testing.T) {
	e := NewEngine(4)
	for i := 0; i < 3; i++ {
		c, release, err := e.acquire([]routeCandidate{candidate("c", 1, 1)}, nil, time.Now(), func(int) int { return 0 })
		if err != nil || c == nil {
			t.Fatal(err)
		}
		release(context.Canceled, true)
	}
	health := e.channelHealth("c")
	if health.Calls != 3 || health.Failures != 3 || health.Status != "ready" || health.Verified || health.LastErrorCode != "request_cancelled" {
		t.Fatal(health)
	}
	_, release, err := e.acquire([]routeCandidate{candidate("c", 1, 1)}, nil, time.Now(), func(int) int { return 0 })
	if err != nil {
		t.Fatal(err)
	}
	release(nil, true)
	health = e.channelHealth("c")
	if !health.Verified || health.LastSuccessAt == nil || health.LastErrorCode != "" {
		t.Fatal(health)
	}
}
