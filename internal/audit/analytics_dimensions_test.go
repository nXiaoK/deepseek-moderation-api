package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"testing"
	"time"
)

func TestAnalyticsDimensionsOutcomesAndPercentiles(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now()
	l := AuditLog{ID: "request", Kind: "production", PolicyID: "policy", ClientID: "client", AttemptCount: 2, LatencyMS: 400, CreatedAt: now}
	if err := s.Record(ctx, l, "", 30); err != nil {
		t.Fatal(err)
	}
	usage, _ := json.Marshal(Usage{ActualModel: "actual-model", Reported: true, PromptTokens: 100, CompletionTokens: 10, TotalTokens: 110})
	for i, latency := range []int{100, 200, 300} {
		code := ""
		if i == 1 {
			code = "upstream_timeout"
		}
		if _, err := s.DB.ExecContext(ctx, `INSERT INTO audit_costs(id,request_id,channel_id,client_id,kind,policy_id,model,started_at,budget_date,reserved_pico,amount_pico,status,usage,latency_ms,request_sent,outcome_known,error_code) VALUES($1,'request','channel','client','production','policy','alias',$2,CURRENT_DATE,0,0,'calculated',$3,$4,TRUE,TRUE,$5)`, fmt.Sprint(i), now, string(usage), latency, code); err != nil {
			t.Fatal(err)
		}
	}
	f := AnalyticsFilter{From: now.Add(-time.Minute), To: now.Add(time.Minute), Kind: "production", IntervalSeconds: 60, PolicyID: "policy", ClientID: "client", ChannelID: "channel", Model: "actual-model"}
	result, err := s.Analytics(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	m := result.Summary
	if m.Calls != 3 || m.SuccessfulCalls != 2 || m.FailedCalls != 1 || m.OutcomeSamples != 3 || m.P95LatencyMS == nil || math.Abs(*m.P95LatencyMS-290) > 0.01 {
		t.Fatalf("wrong outcomes: %+v", m)
	}
	if len(result.Errors) != 1 || result.Errors[0].Code != "upstream_timeout" || result.Errors[0].Calls != 1 {
		t.Fatal(result.Errors)
	}
	if result.Requests.Records != 1 || result.Requests.Allowed != 1 || result.Requests.Errors != 0 || result.Requests.Fallbacks != 1 {
		t.Fatal("attempt failures became request failures", result.Requests)
	}
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO audit_costs(id,request_id,channel_id,client_id,kind,policy_id,model,started_at,budget_date,reserved_pico,amount_pico,status,usage,latency_ms,request_sent) VALUES('legacy','missing-log','channel','client','production','policy','alias',$1,CURRENT_DATE,0,0,'calculated',$2,400,TRUE)`, now, string(usage)); err != nil {
		t.Fatal(err)
	}
	result, err = s.Analytics(ctx, f)
	if err != nil || result.Summary.Calls != 4 || result.Summary.OutcomeSamples != 3 {
		t.Fatal("unknown outcome treated as success", result.Summary, err)
	}
	f.ClientID = "another"
	result, err = s.Analytics(ctx, f)
	if err != nil || result.Summary.Calls != 0 || result.Requests.Records != 0 || len(result.AvailableModels) != 0 {
		t.Fatal("dimension filter ignored", result, err)
	}
}
