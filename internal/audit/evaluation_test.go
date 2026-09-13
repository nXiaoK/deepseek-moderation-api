package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func evaluationTestServer(t *testing.T) (*Server, Policy, func(string, string, any, int) []byte) {
	t.Helper()
	s := testStore(t)
	p, _ := configureBillingPolicy(t, s, 0)
	app, err := NewServer(s, "http://localhost:8090", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	if _, err := s.DB.Exec("INSERT INTO admin_sessions(token_hash,username,csrf,expires_at) VALUES($1,'admin','csrf',NOW()+INTERVAL '1 hour')", digest("session")); err != nil {
		t.Fatal(err)
	}
	call := func(method, path string, body any, status int) []byte {
		t.Helper()
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, bytes.NewReader(raw))
		r.Header.Set("Origin", app.Origin)
		r.Header.Set("Cookie", "audit_session=session")
		r.Header.Set("X-CSRF-Token", "csrf")
		w := httptest.NewRecorder()
		app.Handler().ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s: %d want %d: %s", method, path, w.Code, status, w.Body.String())
		}
		return w.Body.Bytes()
	}
	return app, p, call
}
func waitEvaluation(t *testing.T, app *Server, id string) EvaluationRun {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		run, _, err := app.Store.evaluationRun(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		app.evaluationMu.Lock()
		_, active := app.evaluationCancels[id]
		app.evaluationMu.Unlock()
		if run.Status != "queued" && run.Status != "running" && !active {
			return run
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("evaluation did not finish")
	return EvaluationRun{}
}
func TestEvaluationSamplesRunScoresAndSnapshot(t *testing.T) {
	app, p, call := evaluationTestServer(t)
	app.Engine.Client = &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(cachedTestResponse))}, nil
	})}
	ids := []string{}
	for _, expected := range []string{"allow", "flagged"} {
		raw := call("POST", "/admin/evaluation/samples", map[string]any{"name": "=sample " + expected, "input": "private sample " + expected, "expected": expected, "note": "test"}, 200)
		var result map[string]string
		_ = json.Unmarshal(raw, &result)
		ids = append(ids, result["id"])
	}
	var cipher []byte
	if err := app.Store.DB.QueryRow("SELECT input_cipher FROM evaluation_samples WHERE id=$1", ids[0]).Scan(&cipher); err != nil || bytes.Contains(cipher, []byte("private sample")) {
		t.Fatal("sample not encrypted", err)
	}
	raw := call("POST", "/admin/evaluation/runs", map[string]any{"name": "comparison", "policy_id": p.ID, "sample_ids": ids, "channel_ids": []string{""}, "repetitions": 2, "max_cost_cny": "5"}, 201)
	var created map[string]string
	_ = json.Unmarshal(raw, &created)
	id := created["id"]
	run := waitEvaluation(t, app, id)
	if run.Status != "completed" || run.Completed != 4 {
		t.Fatal(run)
	}
	var detail struct {
		Scores  []EvaluationScore  `json:"scores"`
		Results []EvaluationResult `json:"results"`
	}
	if err := json.Unmarshal(call("GET", "/admin/evaluation/runs/"+id, nil, 200), &detail); err != nil {
		t.Fatal(err)
	}
	if len(detail.Results) != 4 || len(detail.Scores) != 1 || detail.Scores[0].Valid != 4 || detail.Scores[0].FalseNegatives != 2 || detail.Scores[0].Errors != 0 || detail.Scores[0].Correct != 2 {
		t.Fatal(detail)
	}
	if err := app.Store.DB.QueryRow("SELECT plan_cipher FROM evaluation_runs WHERE id=$1", id).Scan(&cipher); err != nil || bytes.Contains(cipher, []byte("private sample")) {
		t.Fatal("plan not encrypted", err)
	}
	call("PUT", "/admin/evaluation/samples/"+ids[0], map[string]any{"name": "edited", "input": "changed after run", "expected": "allow", "note": "", "expected_revision": 1}, 200)
	var snapshot EvaluationSample
	if err := json.Unmarshal(call("GET", "/admin/evaluation/runs/"+id+"/samples/"+ids[0], nil, 200), &snapshot); err != nil || snapshot.Input != "private sample allow" {
		t.Fatal("snapshot changed", snapshot, err)
	}
	exported := call("GET", "/admin/evaluation/runs/"+id+"/export", nil, 200)
	if !bytes.Contains(exported, []byte("'=sample allow")) {
		t.Fatal("CSV formula not escaped")
	}
	call("DELETE", "/admin/evaluation/samples/"+ids[0], nil, 200)
	call("GET", "/admin/evaluation/runs/"+id+"/samples/"+ids[0], nil, 200)
	call("POST", "/admin/evaluation/samples/import", map[string]bool{"builtin": true}, 200)
	var imported map[string]int
	_ = json.Unmarshal(call("POST", "/admin/evaluation/samples/import", map[string]bool{"builtin": true}, 200), &imported)
	if imported["imported"] != 0 {
		t.Fatal("builtin import overwrote existing samples")
	}
	w := httptest.NewRecorder()
	app.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/admin/evaluation/runs/"+id, nil))
	if w.Code != 401 {
		t.Fatal("report lacks authentication")
	}
}
func TestEvaluationBudgetAndCancellation(t *testing.T) {
	app, p, call := evaluationTestServer(t)
	var calls atomic.Int32
	entered := make(chan struct{}, 1)
	app.Engine.Client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		entered <- struct{}{}
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	raw := call("POST", "/admin/evaluation/samples", map[string]string{"name": "sample", "input": "hello", "expected": "allow"}, 200)
	var sample map[string]string
	_ = json.Unmarshal(raw, &sample)
	body := map[string]any{"name": "budget", "policy_id": p.ID, "sample_ids": []string{sample["id"]}, "channel_ids": []string{""}, "repetitions": 2, "max_cost_cny": "0"}
	var created map[string]string
	_ = json.Unmarshal(call("POST", "/admin/evaluation/runs", body, 201), &created)
	if run := waitEvaluation(t, app, created["id"]); run.Status != "budget_exhausted" || calls.Load() != 0 {
		t.Fatal("budget failed", run)
	}
	body["max_cost_cny"] = "5"
	_ = json.Unmarshal(call("POST", "/admin/evaluation/runs", body, 201), &created)
	id := created["id"]
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("model did not start")
	}
	call("POST", "/admin/evaluation/runs", body, 409)
	call("POST", "/admin/evaluation/runs/"+id+"/cancel", nil, 200)
	run := waitEvaluation(t, app, id)
	if run.Status != "cancelled" || calls.Load() != 1 {
		t.Fatal("cancel started another call", run, calls.Load())
	}
	results, err := app.Store.evaluationResults(context.Background(), id)
	if err != nil || len(results) != 2 || results[0].RequestID == "" || results[1].Status != "skipped" {
		t.Fatal(results, err)
	}
}
func TestEvaluationScoresSeparateFailuresAndUnlabelled(t *testing.T) {
	confidence := 0.8
	results := []EvaluationResult{
		{SampleID: "a", Status: "completed", Expected: "allow", Flagged: true, Confidence: &confidence},
		{SampleID: "a", Status: "completed", Expected: "allow", Flagged: false, Confidence: &confidence},
		{SampleID: "b", Status: "completed", Expected: "flagged", Flagged: false, Confidence: &confidence},
		{SampleID: "c", Status: "error", Expected: "allow"},
		{SampleID: "d", Status: "completed", Expected: "manual", Confidence: &confidence},
		{SampleID: "e", Status: "skipped", Expected: "allow"},
	}
	scores, err := evaluationScores(results)
	if err != nil || len(scores) != 1 {
		t.Fatal(err)
	}
	s := scores[0]
	if s.Valid != 4 || s.Processed != 5 || s.Labelled != 3 || s.FalsePositives != 1 || s.FalseNegatives != 1 || s.Errors != 1 || s.UnstableSamples != 1 {
		t.Fatal(s)
	}
}
func TestEvaluationRecoveryKeepsInterruptedRequestLinks(t *testing.T) {
	app, p, _ := evaluationTestServer(t)
	ctx := context.Background()
	plan := evaluationPlan{Policy: p, MaxCost: picoPerCNY}
	raw, _ := json.Marshal(plan)
	for _, status := range []string{"running", "cancelled"} {
		id := "recover_" + status
		if _, err := app.Store.DB.Exec("INSERT INTO evaluation_runs(id,name,username,policy_id,policy_name,status,total,max_cost_pico,plan_cipher) VALUES($1,'recovery','admin',$2,'policy',$3,2,0,$4)", id, p.ID, status, app.Store.Vault.Seal(string(raw), "evaluation-run:"+id)); err != nil {
			t.Fatal(err)
		}
		if _, err := app.Store.DB.Exec(`INSERT INTO evaluation_results(run_id,sequence,sample_id,sample_name,target,iteration,expected,status,payload) VALUES($1,0,'s','sample','',1,'allow','running','{"request_id":"audit_before_restart"}'),($1,1,'s','sample','',2,'allow','pending','{}')`, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := app.Store.RecoverEvaluations(ctx); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"running", "cancelled"} {
		run, _, err := app.Store.evaluationRun(ctx, "recover_"+status)
		if err != nil {
			t.Fatal(err)
		}
		want := "interrupted"
		if status == "cancelled" {
			want = status
		}
		if run.Status != want || run.Completed != 1 {
			t.Fatal(run)
		}
		results, err := app.Store.evaluationResults(ctx, run.ID)
		if err != nil || results[0].RequestID != "audit_before_restart" || results[0].Status != "interrupted" || results[1].Status != "skipped" {
			t.Fatal(results, err)
		}
	}
}
