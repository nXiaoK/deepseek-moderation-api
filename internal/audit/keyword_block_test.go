package audit

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestKeywordBlockMatchingAndValidation(t *testing.T) {
	cfg := DefaultSettings()
	cfg.KeywordBlockEnabled = true
	cfg.BlockKeywords = []string{"hi", "指定短语", "a.b"}
	for _, mode := range []string{"", "contains", "exact"} {
		cfg.KeywordBlockMatchMode = mode
		for _, tc := range []struct {
			input           string
			exact, contains bool
		}{
			{"hi", true, true}, {"hi xxx", false, true}, {"xxx hi", false, true},
			{" hi ", false, true}, {"hi\n", false, true}, {"HI", false, false},
			{"指定短语", true, true}, {"前文指定短语后文", false, true},
			{"a.b", true, true}, {"axb", false, false}, {"", false, false},
		} {
			want := tc.contains
			if mode == "exact" {
				want = tc.exact
			}
			if got := cfg.blocksKeywords(tc.input); got != want {
				t.Errorf("mode %q input %q: got %v want %v", mode, tc.input, got, want)
			}
		}
	}
	cfg.KeywordBlockEnabled = false
	if cfg.blocksKeywords("hi") {
		t.Fatal("disabled rule matched")
	}
	cfg.KeywordBlockEnabled = true
	for _, keywords := range [][]string{nil, {""}, {" \t"}} {
		cfg.BlockKeywords = keywords
		if cfg.blocksKeywords("hi") {
			t.Fatal("empty rule matched")
		}
	}
	for _, keywords := range [][]string{{""}, {" \t"}, {strings.Repeat("字", 1001)}, make([]string, 101)} {
		cfg.BlockKeywords = keywords
		if cfg.Validate() == nil {
			t.Fatal("invalid keyword list accepted")
		}
	}
	cfg.BlockKeywords = make([]string, 17)
	for i := range cfg.BlockKeywords {
		cfg.BlockKeywords[i] = strings.Repeat("字", 1000)
	}
	if cfg.Validate() == nil {
		t.Fatal("total keyword limit not enforced")
	}
	cfg.BlockKeywords = []string{"hi"}
	cfg.KeywordBlockMatchMode = "regex"
	if cfg.Validate() == nil {
		t.Fatal("invalid match mode accepted")
	}
}

func TestKeywordBlockProductionTrialLogsAndGuards(t *testing.T) {
	app, p, call := evaluationTestServer(t)
	ctx := context.Background()
	p.Config.KeywordBlockEnabled = true
	p.Config.KeywordBlockMatchMode = "exact"
	p.Config.BlockKeywords = []string{"hi"}
	p.Config.KeywordIgnoreEnabled = true
	p.Config.IgnoreKeywords = []string{"hi"}
	p.Config.Threshold = 1
	p.Config.StoreInput = true
	p.Config.ResultCacheTTL = 60
	if err := app.Store.SaveConfig(ctx, "admin", p.ID, p.Name, p.Revision, p.Config); err != nil {
		t.Fatal(err)
	}
	p, _ = app.Store.Policy(ctx, p.ID)
	token, err := app.Store.CreateKey(ctx, "admin", "blocked client", []string{p.ID}, 10000)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := app.Store.AuthenticateKey(ctx, token)
	if _, err := app.Store.DB.Exec("INSERT INTO client_budgets(client_id,daily_limit,monthly_limit) VALUES($1,0,0)", key.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Store.DB.Exec("DELETE FROM model_prices"); err != nil {
		t.Fatal(err)
	}
	calls := 0
	app.Engine.Client = &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(cachedTestResponse))}, nil
	})}
	request := func(input any, secret string) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(map[string]any{"model": p.Alias, "input": input})
		r := httptest.NewRequest("POST", "/v1/moderations", strings.NewReader(string(raw)))
		r.Header.Set("Authorization", "Bearer "+secret)
		w := httptest.NewRecorder()
		app.Handler().ServeHTTP(w, r)
		return w
	}
	for _, input := range []any{"hi", json.RawMessage(`[{"type":"text","text":"hi"},{"type":"image_url","image_url":{"url":"data:image/png;base64,YQ=="}}]`)} {
		w := request(input, token)
		if w.Code != 200 {
			t.Fatalf("%d: %s", w.Code, w.Body.String())
		}
		var response Response
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		verdict := response.Results[0]
		if !verdict.Flagged || !verdict.Categories["illicit"] || verdict.Scores["illicit"] != 1 || !verdict.Audit.KeywordBlocked || verdict.Audit.KeywordIgnored {
			t.Fatal(verdict)
		}
		if response.AttemptCount != 0 || response.CacheHit || response.ActualModel != "" || response.Usage.TotalTokens != 0 || response.Cost == nil || *response.Cost.AmountCNY != "0" {
			t.Fatal(response)
		}
		if w.Header().Get("X-Audit-Input-Scope") != "keyword-blocked" {
			t.Fatal(w.Header())
		}
		log, err := app.Store.LogDetail(ctx, response.ID)
		if err != nil || !log.KeywordBlocked || log.KeywordIgnored || !log.Flagged || log.Confidence != nil || len(log.Attempts) != 0 || log.ModelOutputStored || log.Input != "hi" || log.Cost == nil || log.Cost.Status != "zero" {
			t.Fatal(log, err)
		}
		var count int
		if err := app.Store.DB.QueryRow("SELECT COUNT(*) FROM audit_costs WHERE request_id=$1", response.ID).Scan(&count); err != nil || count != 0 {
			t.Fatal(count, err)
		}
	}
	logs, total, err := app.Store.Logs(ctx, LogFilter{Page: 1, PageSize: 20, Result: "keyword_blocked"})
	if err != nil || total != 2 || logs[0].Cost == nil || *logs[0].Cost.AmountCNY != "0" {
		t.Fatal(logs, total, err)
	}
	if _, total, err := app.Store.Logs(ctx, LogFilter{Page: 1, PageSize: 20, Result: "flagged"}); err != nil || total != 2 {
		t.Fatal(total, err)
	}
	if raw := call("GET", "/admin/audit-logs/export?result=keyword_blocked", nil, 200); !strings.Contains(string(raw), "关键词阻止") {
		t.Fatal(string(raw))
	}
	var unmatched Response
	w := request("hi xxx", token)
	if err := json.Unmarshal(w.Body.Bytes(), &unmatched); err != nil || w.Code != 200 || unmatched.Results[0].Audit.KeywordBlocked || !unmatched.Results[0].Audit.KeywordIgnored || unmatched.Results[0].Flagged {
		t.Fatal(w.Code, w.Body.String(), err)
	}
	if w := request("hi", "invalid"); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := request(json.RawMessage(`[{"type":"text","text":"hi"},{"type":"image_url","image_url":{"url":"data:image/png;base64,invalid"}}]`), token); w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request("normal input", token); w.Code != 503 || !strings.Contains(w.Body.String(), "pricing_unavailable") {
		t.Fatal(w.Code, w.Body.String())
	}
	if err := app.Store.SetPolicyState(ctx, "admin", p.ID, p.Revision, false); err != nil {
		t.Fatal(err)
	}
	if w := request("hi", token); w.Code != 503 || !strings.Contains(w.Body.String(), "policy_unavailable") {
		t.Fatal(w.Code, w.Body.String())
	}
	app.trialSlots = make(chan struct{})
	trialCfg := p.Config
	trialCfg.KeywordBlockMatchMode = "contains"
	trialCfg.StoreInput = false
	trialCfg.Channels = []ChannelBinding{}
	var trial Response
	raw := call("POST", "/admin/policies/"+p.ID+"/test", map[string]any{"input": "hi xxx", "config": trialCfg}, 200)
	if err := json.Unmarshal(raw, &trial); err != nil || !trial.Results[0].Audit.KeywordBlocked {
		t.Fatal(string(raw), err)
	}
	log, err := app.Store.LogDetail(ctx, trial.ID)
	if err != nil || log.InputStored || log.Input != "" {
		t.Fatal(log, err)
	}
	if calls != 0 {
		t.Fatal("unexpected upstream calls", calls)
	}
}

func TestKeywordBlockExactMissContinuesModelAudit(t *testing.T) {
	app, p, call := evaluationTestServer(t)
	p.Config.KeywordBlockEnabled = true
	p.Config.KeywordBlockMatchMode = "exact"
	p.Config.BlockKeywords = []string{"hi"}
	calls := 0
	app.Engine.Client = &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(cachedTestResponse))}, nil
	})}
	var response Response
	raw := call("POST", "/admin/policies/"+p.ID+"/test", map[string]any{"input": "hi xxx", "config": p.Config}, 200)
	if err := json.Unmarshal(raw, &response); err != nil || response.Results[0].Audit.KeywordBlocked || response.Results[0].Flagged || response.AttemptCount != 1 || calls != 1 {
		t.Fatal(string(raw), calls, err)
	}
}

func TestKeywordBlockEvaluationZeroBudgetAndScores(t *testing.T) {
	app, p, call := evaluationTestServer(t)
	p.Config.KeywordBlockEnabled = true
	p.Config.KeywordBlockMatchMode = "exact"
	p.Config.BlockKeywords = []string{"hi"}
	p.Config.Threshold = 1
	p.Config.Channels = []ChannelBinding{}
	var sample EvaluationSample
	if err := json.Unmarshal(call("POST", "/admin/evaluation/samples", map[string]string{"name": "blocked", "input": "hi", "expected": "flagged"}, 200), &sample); err != nil {
		t.Fatal(err)
	}
	var created map[string]string
	body := map[string]any{"name": "keyword block", "policy_id": p.ID, "config": p.Config, "sample_ids": []string{sample.ID}, "channel_ids": []string{""}, "repetitions": 1, "max_cost_cny": "0"}
	if err := json.Unmarshal(call("POST", "/admin/evaluation/runs", body, 201), &created); err != nil {
		t.Fatal(err)
	}
	run := waitEvaluation(t, app, created["id"])
	if run.Status != "completed" || run.Completed != 1 {
		t.Fatal(run)
	}
	results, err := app.Store.evaluationResults(context.Background(), run.ID)
	if err != nil || len(results) != 1 || !results[0].KeywordBlocked || results[0].Confidence != nil || !results[0].Flagged || results[0].Cost == nil || *results[0].Cost.AmountCNY != "0" {
		t.Fatal(results, err)
	}
	scores, err := evaluationScores(results)
	if err != nil || scores[0].Valid != 1 || scores[0].Correct != 1 || scores[0].Errors != 0 {
		t.Fatal(scores, err)
	}
	if raw := call("GET", "/admin/evaluation/runs/"+run.ID+"/export", nil, 200); !strings.Contains(string(raw), "关键词阻止,true,,1") {
		t.Fatal(string(raw))
	}
}
