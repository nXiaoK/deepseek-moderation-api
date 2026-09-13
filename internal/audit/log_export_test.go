package audit

import (
	"context"
	"strings"
	"testing"
)

func TestLogExportIsFilteredAndContainsNoRawPayloads(t *testing.T) {
	app, p, call := evaluationTestServer(t)
	s := app.Store
	confidence := 0.1
	l := AuditLog{ID: "export-row", Kind: "production", PolicyID: p.ID, Model: "=model", Confidence: &confidence, Reason: "=formula", InputStored: true, ModelOutputStored: true, ModelOutput: "private model payload", Attempts: []AuditAttempt{{ID: "attempt", ModelOutput: "private attempt"}}}
	if err := s.Record(context.Background(), l, "private input payload", 30); err != nil {
		t.Fatal(err)
	}
	data := string(call("GET", "/admin/audit-logs/export?request_id=export-row", nil, 200))
	if !strings.Contains(data, "export-row") || !strings.Contains(data, "'=formula") || strings.Contains(data, "private input payload") || strings.Contains(data, "private model payload") || strings.Contains(data, "private attempt") {
		t.Fatal("unsafe CSV projection")
	}
	data = string(call("GET", "/admin/audit-logs/export?request_id=missing", nil, 200))
	if strings.Contains(data, "export-row") {
		t.Fatal("export ignored filter")
	}
	call("GET", "/admin/audit-logs/export?from=invalid", nil, 400)
}
func TestCombinedLogFiltersMatchTheSameAttempt(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	l := AuditLog{ID: "mixed-attempts", Model: "b", ChannelID: "channel-b", Attempts: []AuditAttempt{{Model: "a", ChannelID: "channel-a", ErrorCode: "upstream_timeout"}, {Model: "b", ChannelID: "channel-b"}}}
	if err := s.Record(ctx, l, "", 30); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		filter LogFilter
		want   int
	}{
		{LogFilter{Model: "a", ChannelID: "channel-a", ErrorCode: "upstream_timeout"}, 1},
		{LogFilter{Model: "a", ChannelID: "channel-b"}, 0},
		{LogFilter{Model: "b", ErrorCode: "upstream_timeout"}, 0},
	} {
		tc.filter.Page, tc.filter.PageSize = 1, 20
		_, total, err := s.Logs(ctx, tc.filter)
		if err != nil || total != tc.want {
			t.Fatal(tc.filter, total, err)
		}
	}
}
