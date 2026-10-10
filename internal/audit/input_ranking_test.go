package audit

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestInputFingerprintExactMatchingAndKeyIsolation(t *testing.T) {
	vault, _ := NewVault(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32)))
	s := &Store{Vault: vault}
	seen := map[string]bool{}
	for _, text := range []string{"hi", "HI", " hi ", "hi\n", "hi xxx", strings.Repeat("字", 120) + "a", strings.Repeat("字", 120) + "b"} {
		fingerprint := s.inputFingerprint(text)
		if !inputFingerprintPattern.MatchString(fingerprint) || seen[fingerprint] || fingerprint != s.inputFingerprint(text) || fingerprint == s.cacheInputDigest(text, nil) {
			t.Fatal("incorrect fingerprint identity or domain", text)
		}
		seen[fingerprint] = true
	}
	otherVault, _ := NewVault(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{8}, 32)))
	other := &Store{Vault: otherVault}
	if s.inputFingerprint("hi") == other.inputFingerprint("hi") {
		t.Fatal("fingerprint is not keyed")
	}
}

func TestInputRankingFilterValidation(t *testing.T) {
	now := time.Now()
	f, err := parseInputRankingFilter(url.Values{}, now)
	if err != nil || f.MinCount != 2 || f.Page != 1 || f.PageSize != 20 || f.To.Sub(f.From) != 24*time.Hour {
		t.Fatal(f, err)
	}
	for _, query := range []string{"kind=test", "kind=all", "model=model", "channel_id=channel", "page=0", "page_size=101", "min_count=0", "min_count=1.5", "min_count=1000000001", "range=custom&from=bad&to=bad"} {
		q, _ := url.ParseQuery(query)
		if _, err := parseInputRankingFilter(q, now); err == nil {
			t.Fatal("invalid filter accepted", query)
		}
	}
	for _, value := range []string{"short", strings.Repeat("A", 64), strings.Repeat("a", 65)} {
		if _, err := parseLogFilter(url.Values{"input_fingerprint": {value}}); errorCode(err) != "invalid_filter" {
			t.Fatal(value, err)
		}
	}
}

func TestInputRankingGroupingOutcomesRetentionAndFilters(t *testing.T) {
	app, _, call := evaluationTestServer(t)
	s := app.Store
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	sequence := 0
	record := func(text, policy, client, outcome string, stored bool) string {
		id := fmt.Sprintf("ranking-%d", sequence)
		created := at.Add(time.Duration(sequence) * time.Second)
		sequence++
		score := .1
		l := AuditLog{ID: id, Kind: "production", PolicyID: policy, ClientID: client, InputStored: stored, Confidence: &score, InputFingerprint: s.inputFingerprint(text), CreatedAt: created, Request: &AuditRequest{Stage: "completed", TextChars: utf8.RuneCountInString(text)}}
		switch outcome {
		case "blocked":
			l.Flagged, l.KeywordBlocked, l.Confidence = true, true, nil
		case "ignored":
			l.KeywordIgnored, l.Confidence = true, nil
		case "hit":
			l.Flagged = true
		case "allow":
			l.CacheHit, l.AttemptCount = true, 3
		case "error":
			l.Confidence, l.ErrorCode, l.Request.Stage = nil, "upstream_timeout", "audit"
		}
		if err := s.Record(ctx, l, text, 30); err != nil {
			t.Fatal(err)
		}
		if _, err := s.DB.Exec("UPDATE audit_requests SET created_at=$2 WHERE id=$1", id, created); err != nil {
			t.Fatal(err)
		}
		return id
	}
	for i, outcome := range []string{"blocked", "ignored", "hit", "allow", "error"} {
		policy, client := "p1", "c1"
		if i >= 3 {
			policy, client = "p2", "c2"
		}
		record("hi", policy, client, outcome, true)
	}
	for _, text := range []string{"hi xxx", "HI", strings.Repeat("字", 120) + "a", strings.Repeat("字", 120) + "b", "=SUM(1,2)"} {
		for range 2 {
			record(text, "p1", "c1", "allow", text != "HI")
		}
	}
	record(" hi ", "p1", "c1", "allow", true)
	record("hi\n", "p1", "c1", "allow", true)
	f := InputRankingFilter{From: at, To: at.Add(time.Hour), Page: 1, PageSize: 20, MinCount: 2}
	expired := record("hi", "p1", "c1", "allow", true)
	if _, err := s.DB.Exec("UPDATE audit_requests SET expires_at=NOW()-INTERVAL '1 second' WHERE id=$1", expired); err != nil {
		t.Fatal(err)
	}
	outside := record("hi", "p1", "c1", "allow", true)
	if _, err := s.DB.Exec("UPDATE audit_requests SET created_at=$2 WHERE id=$1", outside, f.To); err != nil {
		t.Fatal(err)
	}
	trial := record("hi", "p1", "c1", "allow", true)
	if _, err := s.DB.Exec("UPDATE audit_requests SET kind='test' WHERE id=$1", trial); err != nil {
		t.Fatal(err)
	}
	out, err := s.InputRanking(ctx, f, false)
	if err != nil || out.Total != 6 || len(out.Items) != 6 || out.Summary.IndexedRequests != 17 || out.Summary.UniqueInputs != 8 || out.Summary.RepeatedInputs != 6 || out.Summary.RepeatRequests != 9 {
		t.Fatal(out, err)
	}
	hi := out.Items[0]
	if hi.Fingerprint != s.inputFingerprint("hi") || hi.Occurrences != 5 || hi.Flagged != 2 || hi.KeywordBlocked != 1 || hi.KeywordIgnored != 1 || hi.ModelFlagged != 1 || hi.ModelAllowed != 1 || hi.Errors != 1 || hi.Clients != 2 || hi.CacheHits != 1 || hi.Preview == nil || *hi.Preview != "hi" || hi.Input != nil {
		t.Fatal(hi)
	}
	for _, item := range out.Items {
		if item.Fingerprint == s.inputFingerprint("HI") && (item.Preview != nil || item.SampleRequestID != "") {
			t.Fatal("unsaved input exposed", item)
		}
		if item.TextChars == 121 && (item.Preview == nil || utf8.RuneCountInString(*item.Preview) != 120) {
			t.Fatal("preview changed identity or length", item)
		}
	}
	f.Page, f.PageSize = 99, 2
	out, err = s.InputRanking(ctx, f, false)
	if err != nil || out.Page != 3 || len(out.Items) != 2 {
		t.Fatal("pagination not clamped", out, err)
	}
	f.Page, f.PageSize, f.PolicyID = 1, 20, "p2"
	out, err = s.InputRanking(ctx, f, false)
	if err != nil || out.Total != 1 || out.Items[0].Occurrences != 2 || out.Items[0].Errors != 1 {
		t.Fatal(out, err)
	}
	f.PolicyID, f.ClientID = "", "c2"
	out, err = s.InputRanking(ctx, f, false)
	if err != nil || out.Total != 1 || out.Items[0].Occurrences != 2 {
		t.Fatal(out, err)
	}
	f.ClientID, f.MinCount = "", 1
	out, err = s.InputRanking(ctx, f, false)
	if err != nil || out.Total != 8 {
		t.Fatal(out, err)
	}
	q := url.Values{"range": {"custom"}, "from": {f.From.Format(time.RFC3339Nano)}, "to": {f.To.Format(time.RFC3339Nano)}}
	var apiOut InputRankingResponse
	if err := json.Unmarshal(call("GET", "/admin/analytics/input-ranking?"+q.Encode(), nil, 200), &apiOut); err != nil || apiOut.Total != 6 {
		t.Fatal(apiOut, err)
	}
	var detail InputRankingItem
	if err := json.Unmarshal(call("GET", "/admin/analytics/input-ranking/"+hi.Fingerprint+"?"+q.Encode(), nil, 200), &detail); err != nil || detail.Input == nil || *detail.Input != "hi" {
		t.Fatal(detail, err)
	}
	q.Set("policy_id", "missing")
	call("GET", "/admin/analytics/input-ranking/"+hi.Fingerprint+"?"+q.Encode(), nil, 404)
	q.Del("policy_id")
	csv := call("GET", "/admin/analytics/input-ranking/export?"+q.Encode(), nil, 200)
	if !strings.Contains(string(csv), "'=SUM(1,2)") || !strings.Contains(string(csv), "原文未保存") {
		t.Fatal(string(csv))
	}
	logs, total, err := s.Logs(ctx, LogFilter{Page: 1, PageSize: 20, Kind: "production", From: f.From.Format(time.RFC3339Nano), To: f.To.Format(time.RFC3339Nano), InputFingerprint: hi.Fingerprint, KeywordIgnore: "include"})
	if err != nil || total != 5 || len(logs) != 5 {
		t.Fatal(total, logs, err)
	}
	for _, path := range []string{"/admin/analytics/input-ranking", "/admin/analytics/input-ranking/export", "/admin/analytics/input-ranking/" + hi.Fingerprint} {
		w := httptest.NewRecorder()
		app.Handler().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 401 {
			t.Fatal("missing ranking authentication", path, w.Code)
		}
	}
}

func TestInputRankingProductionCaptureAndValidationGuards(t *testing.T) {
	app, p, call := evaluationTestServer(t)
	ctx := context.Background()
	p.Config.KeywordBlockEnabled, p.Config.KeywordBlockMatchMode, p.Config.BlockKeywords = true, "exact", []string{"hi"}
	p.Config.StoreInput = false
	if err := app.Store.SaveConfig(ctx, "admin", p.ID, p.Name, p.Revision, p.Config); err != nil {
		t.Fatal(err)
	}
	token, err := app.Store.CreateKey(ctx, "admin", "ranking", []string{p.ID}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	request := func(input any, secret string, want int) Response {
		raw, _ := json.Marshal(map[string]any{"model": p.Alias, "input": input})
		r := httptest.NewRequest("POST", "/v1/moderations", bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer "+secret)
		w := httptest.NewRecorder()
		app.Handler().ServeHTTP(w, r)
		if w.Code != want {
			t.Fatal(w.Code, w.Body.String())
		}
		var response Response
		if want == 200 {
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
		}
		return response
	}
	request("hi", token, 200)
	request("hi", token, 200)
	request(json.RawMessage(`[{"type":"text","text":"hi"},{"type":"image_url","image_url":{"url":"data:image/png;base64,YQ=="}}]`), token, 200)
	request("hi", "invalid", 401)
	request(json.RawMessage(`[{"type":"text","text":"hi"},{"type":"image_url","image_url":{"url":"data:image/png;base64,invalid"}}]`), token, 400)
	call("POST", "/admin/policies/"+p.ID+"/test", map[string]any{"input": "hi", "config": p.Config}, 200)
	var out InputRankingResponse
	if err := json.Unmarshal(call("GET", "/admin/analytics/input-ranking", nil, 200), &out); err != nil || out.Total != 1 || out.Summary.IndexedRequests != 3 || out.Items[0].Occurrences != 3 || out.Items[0].Preview != nil || out.Items[0].KeywordBlocked != 3 {
		t.Fatal(out, err)
	}
	app.Engine.Client = &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(cachedTestResponse))}, nil
	})}
	request(json.RawMessage(`[{"type":"image_url","image_url":{"url":"data:image/png;base64,YQ=="}}]`), token, 200)
	if err := json.Unmarshal(call("GET", "/admin/analytics/input-ranking", nil, 200), &out); err != nil || out.Summary.IndexedRequests != 3 {
		t.Fatal("pure image counted", out, err)
	}
	var cipherCount int
	if err := app.Store.DB.QueryRow("SELECT COUNT(*) FROM audit_requests WHERE input_fingerprint IS NOT NULL AND input_cipher IS NOT NULL").Scan(&cipherCount); err != nil || cipherCount != 0 {
		t.Fatal("fingerprinting retained raw input", cipherCount, err)
	}
}

func TestInputRankingCountsRetriesAndCacheOnce(t *testing.T) {
	app, p, _ := evaluationTestServer(t)
	ctx := context.Background()
	keys, err := app.Store.Keys(ctx)
	if err != nil || len(keys) != 1 {
		t.Fatal(keys, err)
	}
	clientID := keys[0].ID
	channels, err := app.Store.Channels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	backup := createTestChannel(t, app.Store, channels[0].CredentialID, "ranking backup", "deepseek-flash")
	p.Config.Channels = append(p.Config.Channels, ChannelBinding{backup.ID, 2, 100, true})
	p.Config.ResultCacheTTL = 60
	p.Config.KeywordIgnoreEnabled, p.Config.IgnoreKeywords = true, []string{"ignore-me"}
	if err := app.Store.SaveConfig(ctx, "admin", p.ID, p.Name, p.Revision, p.Config); err != nil {
		t.Fatal(err)
	}
	p, channels, err = app.Store.RouteSnapshot(ctx, p.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	calls, fail := 0, false
	app.Engine.Client = &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		calls++
		body := cachedTestResponse
		if calls == 1 || fail {
			body = `{"choices":[{"message":{"content":"invalid"}}]}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	for range 2 {
		res, err := app.runAudit(ctx, p, channels, clientID, "production", "ignore-me", "")
		if err != nil || !res.Results[0].Audit.KeywordIgnored || calls != 0 {
			t.Fatal("ignored input called model", res, calls, err)
		}
	}
	res, err := app.runAudit(ctx, p, channels, clientID, "production", "retry-input", "")
	if err != nil || res.AttemptCount != 2 || calls != 2 {
		t.Fatal("expected two model attempts", res, calls, err)
	}
	res, err = app.runAudit(ctx, p, channels, clientID, "production", "retry-input", "")
	if err != nil || !res.CacheHit || calls != 2 {
		t.Fatal("expected cached request", res, calls, err)
	}
	// Clear the first invalid attempt's cooldown so both routes can fail.
	client := app.Engine.Client
	fail = true
	app.Engine = NewEngine(8)
	app.Engine.Client = client
	if _, err := app.runAudit(ctx, p, channels, clientID, "production", "failed-input", ""); err == nil {
		t.Fatal("expected failed model audit")
	}
	f := InputRankingFilter{From: time.Now().Add(-time.Hour), To: time.Now().Add(time.Hour), Page: 1, PageSize: 20, MinCount: 1}
	out, err := app.Store.InputRanking(ctx, f, false)
	if err != nil || out.Summary.IndexedRequests != 5 || out.Total != 3 {
		t.Fatal(out, err)
	}
	for _, item := range out.Items {
		switch item.Fingerprint {
		case app.Store.inputFingerprint("retry-input"):
			if item.Occurrences != 2 || item.ModelAllowed != 2 || item.CacheHits != 1 {
				t.Fatal("attempts counted as requests", item)
			}
		case app.Store.inputFingerprint("ignore-me"):
			if item.Occurrences != 2 || item.KeywordIgnored != 2 {
				t.Fatal("ignored requests missing", item)
			}
		case app.Store.inputFingerprint("failed-input"):
			if item.Occurrences != 1 || item.Errors != 1 {
				t.Fatal("failed audit missing", item)
			}
		}
	}
}

func TestInputRankingExportLimit(t *testing.T) {
	app, _, call := evaluationTestServer(t)
	_, err := app.Store.DB.Exec(`INSERT INTO audit_requests(id,kind,policy_id,client_id,flagged,error_code,metadata,expires_at,input_fingerprint,input_fingerprint_checked)
 SELECT 'export-'||n||'-'||occurrence,'production','policy','client',FALSE,'','{}',NOW()+INTERVAL '1 day',lpad(to_hex(n),64,'0'),TRUE
 FROM generate_series(1,1001) n CROSS JOIN generate_series(1,2) occurrence`)
	if err != nil {
		t.Fatal(err)
	}
	body := call("GET", "/admin/analytics/input-ranking/export", nil, 400)
	if !strings.Contains(string(body), "export_too_large") {
		t.Fatal(string(body))
	}
	if _, err := app.Store.DB.Exec("DELETE FROM audit_requests WHERE input_fingerprint=lpad(to_hex(1001),64,'0')"); err != nil {
		t.Fatal(err)
	}
	body = call("GET", "/admin/analytics/input-ranking/export", nil, 200)
	rows, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(string(body), "\xef\xbb\xbf"))).ReadAll()
	if err != nil || len(rows) != 1001 {
		t.Fatal("export did not include exactly 1000 groups", len(rows), err)
	}
}

func TestInputFingerprintHistoricalBackfill(t *testing.T) {
	app, _, _ := evaluationTestServer(t)
	s := app.Store
	ctx := context.Background()
	score := .1
	for _, tc := range []struct {
		id, text, stage string
		stored          bool
	}{
		{"saved", "hi", "completed", true}, {"failed", "hi", "audit", true}, {"missing", "hi", "completed", false},
		{"rejected", "hi", "input_validation", true}, {"expired", "hi", "completed", true}, {"corrupt", "hi", "completed", true}, {"old", "hello", "", true},
	} {
		l := AuditLog{ID: tc.id, Kind: "production", InputStored: tc.stored, Confidence: &score}
		if tc.stage != "" {
			l.Request = &AuditRequest{Stage: tc.stage, TextChars: len(tc.text)}
		}
		if tc.id == "failed" {
			l.ErrorCode, l.Confidence = "upstream_timeout", nil
		}
		if err := s.Record(ctx, l, tc.text, 30); err != nil {
			t.Fatal(err)
		}
		if _, err := s.DB.Exec("UPDATE audit_requests SET input_fingerprint_checked=FALSE WHERE id=$1", tc.id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.DB.Exec("UPDATE audit_requests SET expires_at=NOW()-INTERVAL '1 second' WHERE id='expired'"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("UPDATE audit_requests SET input_cipher=$1 WHERE id='corrupt'", []byte("invalid")); err != nil {
		t.Fatal(err)
	}
	f := InputRankingFilter{From: time.Now().Add(-time.Hour), To: time.Now().Add(time.Hour), Page: 1, PageSize: 20, MinCount: 1}
	out, err := s.InputRanking(ctx, f, false)
	if err != nil || out.Summary.BackfillPending != 4 || out.Summary.UnindexedRequests != 5 {
		t.Fatal(out, err)
	}
	if n, err := s.backfillInputFingerprints(ctx, 1); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if n, err := s.backfillInputFingerprints(ctx, 50); err != nil || n != 3 {
		t.Fatal(n, err)
	}
	if n, err := s.backfillInputFingerprints(ctx, 50); err != nil || n != 0 {
		t.Fatal("backfill repeated", n, err)
	}
	out, err = s.InputRanking(ctx, f, false)
	if err != nil || out.Total != 2 || out.Summary.IndexedRequests != 3 || out.Summary.BackfillPending != 0 || out.Summary.UnindexedRequests != 2 || out.Items[0].Occurrences != 2 {
		t.Fatal(out, err)
	}
	if _, err := s.DB.Exec("UPDATE audit_requests SET input_fingerprint=NULL,input_fingerprint_checked=FALSE WHERE id='saved'"); err != nil {
		t.Fatal(err)
	}
	app.StartInputFingerprintWorker()
	app.StartInputFingerprintWorker()
	deadline := time.Now().Add(2 * time.Second)
	for {
		var completed bool
		if err := s.DB.QueryRow("SELECT input_fingerprint IS NOT NULL FROM audit_requests WHERE id='saved'").Scan(&completed); err != nil {
			t.Fatal(err)
		}
		if completed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background backfill did not complete")
		}
		time.Sleep(10 * time.Millisecond)
	}
	app.Close()
}
