package audit

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestLogKeywordAndLatencyFilters(t *testing.T) {
	app, _, call := evaluationTestServer(t)
	ctx := context.Background()
	for _, row := range []AuditLog{
		{ID: "below", LatencyMS: 999},
		{ID: "equal", LatencyMS: 1000},
		{ID: "above", LatencyMS: 1001},
		{ID: "two", LatencyMS: 2000},
		{ID: "three", LatencyMS: 3000, Flagged: true},
		{ID: "failure", LatencyMS: 5001, ErrorCode: "upstream_timeout"},
		{ID: "ignored", LatencyMS: 3001, KeywordIgnored: true},
		{ID: "ignored-error", LatencyMS: 5002, KeywordIgnored: true, ErrorCode: "cost_record_unavailable"},
		{ID: "legacy"},
	} {
		row.Kind = "production"
		row.Attempts = []AuditAttempt{{LatencyMS: 10000}} // Filter root latency, not an attempt.
		if err := app.Store.Record(ctx, row, "", 30); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := app.Store.DB.Exec("UPDATE audit_requests SET metadata=metadata-'latency_ms'-'keyword_ignored' WHERE id='legacy'"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		query string
		want  []string
	}{
		{"", []string{"below", "equal", "above", "two", "three", "failure", "legacy"}},
		{"keyword_ignore=include", []string{"below", "equal", "above", "two", "three", "failure", "legacy", "ignored", "ignored-error"}},
		{"keyword_ignore=only", []string{"ignored", "ignored-error"}},
		{"result=keyword_ignored", []string{"ignored"}}, // Older bookmarked links still work.
		{"latency_gt_ms=1000", []string{"above", "two", "three", "failure"}},
		{"latency_gt_ms=2000", []string{"three", "failure"}},
		{"latency_gt_ms=3000", []string{"failure"}},
		{"latency_gt_ms=5000", []string{"failure"}},
		{"latency_gt_ms=10000", []string{}},
		{"keyword_ignore=only&latency_gt_ms=3000&result=allow", []string{"ignored"}},
		{"keyword_ignore=include&latency_gt_ms=3000&result=error", []string{"failure", "ignored-error"}},
		{"result=allow&latency_gt_ms=1000", []string{"above", "two"}},
		{"request_id=ignored", []string{}},
		{"request_id=ignored&keyword_ignore=include", []string{"ignored"}},
	} {
		t.Run(tc.query, func(t *testing.T) {
			var response struct {
				Items []AuditLog `json:"items"`
				Total int        `json:"total"`
			}
			if err := json.Unmarshal(call("GET", "/admin/audit-logs?"+tc.query, nil, 200), &response); err != nil {
				t.Fatal(err)
			}
			got := []string{}
			for _, row := range response.Items {
				got = append(got, row.ID)
			}
			sort.Strings(got)
			sort.Strings(tc.want)
			if !reflect.DeepEqual(got, tc.want) || response.Total != len(tc.want) {
				t.Fatal(got, response.Total, tc.want)
			}
			data := call("GET", "/admin/audit-logs/export?"+tc.query, nil, 200)
			rows, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(string(data), "\ufeff"))).ReadAll()
			if err != nil {
				t.Fatal(err)
			}
			exported := []string{}
			for _, row := range rows[1:] {
				exported = append(exported, row[0])
			}
			sort.Strings(exported)
			if !reflect.DeepEqual(exported, tc.want) {
				t.Fatal("CSV filter mismatch", exported, tc.want)
			}
		})
	}
	filter, err := parseLogFilter(url.Values{"latency_gt_ms": {"1000"}, "page_size": {"2"}, "page": {"2"}})
	if err != nil {
		t.Fatal(err)
	}
	items, total, err := app.Store.Logs(ctx, filter)
	if err != nil || len(items) != 2 || total != 4 {
		t.Fatal(items, total, err)
	}
	for _, row := range items {
		if row.KeywordIgnored || row.LatencyMS <= 1000 {
			t.Fatal("pagination lost filters", row)
		}
	}
	// Hiding a list entry must not break direct historical-detail links.
	call("GET", "/admin/audit-logs/ignored", nil, 200)
}

func TestLogFilterRejectsInvalidKeywordAndLatency(t *testing.T) {
	for _, value := range []string{"-1", "1.5", "no", "1e3", "86400001", "9223372036854775808", "1000 OR TRUE"} {
		if _, err := parseLogFilter(url.Values{"latency_gt_ms": {value}}); errorCode(err) != "invalid_filter" {
			t.Fatalf("accepted %q: %v", value, err)
		}
	}
	if _, err := parseLogFilter(url.Values{"keyword_ignore": {"invalid"}}); errorCode(err) != "invalid_filter" {
		t.Fatal(err)
	}
	f, err := parseLogFilter(url.Values{"latency_gt_ms": {"0"}})
	if err != nil || f.LatencyGTMS == nil || *f.LatencyGTMS != 0 {
		t.Fatal(f, err)
	}
}
