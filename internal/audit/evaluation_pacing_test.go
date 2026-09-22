package audit

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func pacingPlan() evaluationPlan {
	return evaluationPlan{
		Policy: Policy{Config: PolicySettings{Channels: []ChannelBinding{{"a", 1, 100, true}, {"b", 2, 100, true}}}},
		Channels: []ModelChannel{
			{ID: "a", RPM: 10, Enabled: true, CredentialActive: true},
			{ID: "b", RPM: 20, Enabled: true, CredentialActive: true},
		},
		Targets: []string{""}, Repetitions: 1, Samples: []EvaluationSample{{}},
	}
}

func TestEvaluationRPMIntervals(t *testing.T) {
	for _, tc := range []struct {
		rpm  int
		want time.Duration
	}{
		{0, 0}, {1, 62 * time.Second}, {10, 8 * time.Second}, {20, 5 * time.Second}, {60, 3 * time.Second},
	} {
		if got := evaluationInterval(tc.rpm); got != tc.want {
			t.Fatalf("RPM %d: %s, want %s", tc.rpm, got, tc.want)
		}
	}
}

func TestEvaluationPacingUsesEachAttemptStartAndTarget(t *testing.T) {
	plan := pacingPlan()
	now := time.Now()
	pacer := evaluationPacer{}
	if delay := pacer.delay(plan, "", "input", now); delay != 0 {
		t.Fatal("first call delayed", delay)
	}
	pacer["a"] = now
	if delay := pacer.delay(plan, "a", "input", now.Add(2*time.Second)); delay != 6*time.Second {
		t.Fatal("call time was not deducted", delay)
	}
	if delay := pacer.delay(plan, "b", "input", now); delay != 0 {
		t.Fatal("unrelated channel was delayed", delay)
	}
	// A fallback started later than the primary and must keep its own spacing.
	pacer["b"] = now.Add(6 * time.Second)
	if delay := pacer.delay(plan, "", "input", now.Add(7*time.Second)); delay != 4*time.Second {
		t.Fatal("fallback interval lost", delay)
	}
	if delay := pacer.delay(plan, "", "input", now.Add(12*time.Second)); delay != 0 {
		t.Fatal("slow calls incurred an extra full delay", delay)
	}
	plan.Policy.Config.Channels[1].Enabled = false
	if delay := pacer.delay(plan, "", "input", now.Add(7*time.Second)); delay != time.Second {
		t.Fatal("disabled binding delayed evaluation", delay)
	}
	plan.Channels[0].RPM = 0
	if delay := pacer.delay(plan, "", "input", now); delay != 0 {
		t.Fatal("unlimited RPM delayed", delay)
	}
	plan.Channels[0].RPM = 10
	plan.Policy.Config.KeywordIgnoreEnabled = true
	plan.Policy.Config.IgnoreKeywords = []string{"skip"}
	if delay := pacer.delay(plan, "", "skip", now); delay != 0 {
		t.Fatal("keyword bypass delayed", delay)
	}
}

func TestEvaluationPacingWaitAndCancellation(t *testing.T) {
	start := time.Now()
	if err := waitEvaluationDelay(context.Background(), 20*time.Millisecond); err != nil || time.Since(start) < 20*time.Millisecond {
		t.Fatal("wait ended before its interval", time.Since(start), err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- waitEvaluationDelay(ctx, time.Minute) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation waited for pacing timer")
	}
	if err := waitEvaluationDelay(ctx, 0); !errors.Is(err, context.Canceled) {
		t.Fatal("zero delay ignored cancellation", err)
	}
}

func TestEvaluationTimeoutIncludesPacing(t *testing.T) {
	plan := pacingPlan()
	plan.Samples = make([]EvaluationSample, 100)
	plan.Repetitions = 2
	if got := evaluationTimeout(plan); got != 30*time.Minute+200*8*time.Second {
		t.Fatal("paced run retains fixed deadline", got)
	}
	plan.Retries = 2
	if got := evaluationTimeout(plan); got != 30*time.Minute+600*8*time.Second+200*3*time.Second {
		t.Fatal("retry pacing and backoff not included in timeout", got)
	}
	plan.Retries = 0
	plan.Targets = []string{"a", "b"}
	plan.Repetitions = 1
	if got := evaluationTimeout(plan); got != 30*time.Minute+100*13*time.Second {
		t.Fatal("targets not included in timeout", got)
	}
	plan.Channels[0].RPM, plan.Channels[1].RPM = 0, 0
	if got := evaluationTimeout(plan); got != 30*time.Minute {
		t.Fatal("unlimited run timeout changed", got)
	}
}

func TestEvaluationWorkerPacesAndCanCancelDuringWait(t *testing.T) {
	for _, cancelDuringWait := range []bool{false, true} {
		t.Run(map[bool]string{false: "completes", true: "cancelled"}[cancelDuringWait], func(t *testing.T) {
			app, policy, call := evaluationTestServer(t)
			channels, err := app.Store.Channels(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			channel := channels[0]
			channel.RPM = 10
			if _, err := app.Store.SaveChannel(context.Background(), "admin", channel); err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			var starts []time.Time
			app.Engine.Client = &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
				mu.Lock()
				starts = append(starts, time.Now())
				mu.Unlock()
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(cachedTestResponse))}, nil
			})}
			var sample, created map[string]string
			_ = json.Unmarshal(call("POST", "/admin/evaluation/samples", map[string]string{"name": "pace", "input": "hello", "expected": "allow"}, 200), &sample)
			_ = json.Unmarshal(call("POST", "/admin/evaluation/runs", map[string]any{"name": "paced", "policy_id": policy.ID, "sample_ids": []string{sample["id"]}, "channel_ids": []string{channel.ID}, "repetitions": 2, "max_cost_cny": "5"}, 201), &created)
			if cancelDuringWait {
				deadline := time.Now().Add(5 * time.Second)
				waiting := false
				for time.Now().Before(deadline) {
					run, _, err := app.Store.evaluationRun(context.Background(), created["id"])
					if err != nil {
						t.Fatal(err)
					}
					if strings.Contains(run.Message, "RPM") {
						waiting = true
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				if !waiting {
					t.Fatal("evaluation did not enter pacing wait")
				}
				call("POST", "/admin/evaluation/runs/"+created["id"]+"/cancel", nil, 200)
			}
			run := waitEvaluation(t, app, created["id"])
			mu.Lock()
			defer mu.Unlock()
			if cancelDuringWait {
				if run.Status != "cancelled" || len(starts) != 1 {
					t.Fatal("cancel dispatched another call", run, starts)
				}
			} else if run.Status != "completed" || len(starts) != 2 || starts[1].Sub(starts[0]) < 8*time.Second-10*time.Millisecond {
				t.Fatal("worker did not pace requests", run, starts)
			}
		})
	}
}

func TestEvaluationCleanupPreservesPacedRun(t *testing.T) {
	app, policy, _ := evaluationTestServer(t)
	plan := pacingPlan()
	plan.Policy.ID = policy.ID
	plan.Samples = make([]EvaluationSample, 100)
	plan.Repetitions = 2
	raw, _ := json.Marshal(plan)
	if _, err := app.Store.DB.Exec("INSERT INTO evaluation_runs(id,name,username,policy_id,policy_name,status,total,max_cost_pico,plan_cipher,created_at) VALUES('paced-live','paced','admin',$1,'policy','running',200,0,$2,NOW()-INTERVAL '40 minutes')", policy.ID, app.Store.Vault.Seal(string(raw), "evaluation-run:paced-live")); err != nil {
		t.Fatal(err)
	}
	if err := app.Store.cleanupEvaluations(context.Background()); err != nil {
		t.Fatal(err)
	}
	run, _, err := app.Store.evaluationRun(context.Background(), "paced-live")
	if err != nil || run.Status != "running" {
		t.Fatal("cleanup interrupted paced run", run, err)
	}
	if _, err := app.Store.DB.Exec("UPDATE evaluation_runs SET created_at=NOW()-INTERVAL '70 minutes' WHERE id='paced-live'"); err != nil {
		t.Fatal(err)
	}
	if err := app.Store.cleanupEvaluations(context.Background()); err != nil {
		t.Fatal(err)
	}
	run, _, err = app.Store.evaluationRun(context.Background(), "paced-live")
	if err != nil || run.Status != "interrupted" {
		t.Fatal("expired paced run was not cleaned", run, err)
	}
}
