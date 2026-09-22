package audit

import (
	"context"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestEvaluationRetriesAndContinues(t *testing.T) {
	for _, tc := range []struct {
		name                                     string
		failures, retries, wantCalls, wantErrors int
		useDefault                               bool
	}{
		{"recovers", 1, 2, 3, 0, false},
		{"exhausted_default", 3, 2, 4, 1, true},
		{"disabled", 1, 0, 2, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, p, call := evaluationTestServer(t)
			var calls atomic.Int32
			app.Engine.Client = &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
				if int(calls.Add(1)) <= tc.failures {
					return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"temporary failure"}}`))}, nil
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(cachedTestResponse))}, nil
			})}
			var sample EvaluationSample
			if err := json.Unmarshal(call("POST", "/admin/evaluation/samples", map[string]string{"name": "retry", "input": "hello", "expected": "allow"}, 200), &sample); err != nil {
				t.Fatal(err)
			}
			body := map[string]any{"name": "retry", "policy_id": p.ID, "sample_ids": []string{sample.ID}, "channel_ids": []string{p.Config.Channels[0].ChannelID}, "repetitions": 2, "max_cost_cny": "5"}
			if !tc.useDefault {
				body["retries"] = tc.retries
			}
			var created map[string]string
			if err := json.Unmarshal(call("POST", "/admin/evaluation/runs", body, 201), &created); err != nil {
				t.Fatal(err)
			}
			run := waitEvaluation(t, app, created["id"])
			if run.Status != "completed" || run.Completed != 2 || int(calls.Load()) != tc.wantCalls {
				t.Fatal(run, calls.Load())
			}
			_, plan, err := app.Store.evaluationRun(context.Background(), run.ID)
			if err != nil || plan.Retries != tc.retries {
				t.Fatal(plan.Retries, err)
			}
			results, err := app.Store.evaluationResults(context.Background(), run.ID)
			if err != nil || len(results) != 2 {
				t.Fatal(results, err)
			}
			if len(results[0].RequestIDs) != tc.wantCalls-1 || results[1].Status != "completed" || results[0].Cost == nil || results[0].Cost.Status != "pending" {
				t.Fatal(results)
			}
			if tc.wantErrors > 0 && (results[0].Status != "error" || results[0].ErrorCode != "upstream_unavailable") {
				t.Fatal(results[0])
			}
			scores, err := evaluationScores(results)
			if err != nil || scores[0].Errors != tc.wantErrors || scores[0].Processed != 2 || scores[0].PendingCosts != 1 {
				t.Fatal(scores, err)
			}
			// All attempts remain linked, and known charges survive a pending attempt.
			ids := append(append([]string{}, results[0].RequestIDs...), results[1].RequestIDs...)
			costs, err := app.Store.evaluationCosts(context.Background(), ids)
			if err != nil {
				t.Fatal(err)
			}
			known := new(big.Int)
			for _, cost := range costs {
				known.Add(known, cost.known)
			}
			if scores[0].KnownCostCNY != evaluationMoneyString(known) || known.Sign() <= 0 {
				t.Fatal(scores, known)
			}
			for _, id := range ids {
				var n int
				if err := app.Store.DB.QueryRow("SELECT COUNT(*) FROM audit_requests WHERE id=$1", id).Scan(&n); err != nil || n != 1 {
					t.Fatal(id, n, err)
				}
			}
			exported := string(call("GET", "/admin/evaluation/runs/"+run.ID+"/export", nil, 200))
			for _, id := range ids {
				if !strings.Contains(exported, id) {
					t.Fatal("missing CSV request", id)
				}
			}
		})
	}
}

func TestEvaluationRetryBudgetAndValidation(t *testing.T) {
	app, p, call := evaluationTestServer(t)
	var calls atomic.Int32
	app.Engine.Client = &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})}
	var sample EvaluationSample
	_ = json.Unmarshal(call("POST", "/admin/evaluation/samples", map[string]string{"name": "budget", "input": "hello", "expected": "allow"}, 200), &sample)
	policy, channels, err := app.Store.RouteSnapshot(context.Background(), p.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	estimate, err := app.estimateEvaluationTrial(context.Background(), evaluationPlan{Policy: policy, Channels: channels}, "", "hello")
	if err != nil || estimate <= 0 {
		t.Fatal(estimate, err)
	}
	body := map[string]any{"name": "budget", "policy_id": p.ID, "sample_ids": []string{sample.ID}, "channel_ids": []string{""}, "repetitions": 2, "max_cost_cny": picoString(estimate)}
	for _, retries := range []int{-1, 6} {
		body["retries"] = retries
		call("POST", "/admin/evaluation/runs", body, 400)
	}
	body["retries"] = 2
	var created map[string]string
	_ = json.Unmarshal(call("POST", "/admin/evaluation/runs", body, 201), &created)
	run := waitEvaluation(t, app, created["id"])
	if run.Status != "budget_exhausted" || run.Completed != 1 || calls.Load() != 1 {
		t.Fatal(run, calls.Load())
	}
	results, err := app.Store.evaluationResults(context.Background(), run.ID)
	if err != nil || results[0].Status != "error" || results[1].Status != "skipped" {
		t.Fatal(results, err)
	}
}

func TestEvaluationCancelRetryWait(t *testing.T) {
	app, p, call := evaluationTestServer(t)
	var calls atomic.Int32
	app.Engine.Client = &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})}
	var sample EvaluationSample
	_ = json.Unmarshal(call("POST", "/admin/evaluation/samples", map[string]string{"name": "cancel", "input": "hello", "expected": "allow"}, 200), &sample)
	var created map[string]string
	_ = json.Unmarshal(call("POST", "/admin/evaluation/runs", map[string]any{"name": "cancel", "policy_id": p.ID, "sample_ids": []string{sample.ID}, "channel_ids": []string{""}, "repetitions": 2, "max_cost_cny": "5"}, 201), &created)
	deadline := time.Now().Add(5 * time.Second)
	for {
		run, _, err := app.Store.evaluationRun(context.Background(), created["id"])
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(run.Message, "后重试") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("never entered retry wait", run)
		}
		time.Sleep(10 * time.Millisecond)
	}
	call("POST", "/admin/evaluation/runs/"+created["id"]+"/cancel", nil, 200)
	run := waitEvaluation(t, app, created["id"])
	if run.Status != "cancelled" || calls.Load() != 1 {
		t.Fatal(run, calls.Load())
	}
}
