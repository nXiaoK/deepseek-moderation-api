package audit

import (
	"context"
	"fmt"
	"reflect"
	"testing"
)

func TestLogFiltersAndBatchCosts(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("audit_search_%d", i)
		l := AuditLog{ID: id, Kind: "production", Model: "model-a", ChannelID: "final", ModelOutput: "private output", Attempts: []AuditAttempt{{ChannelID: "first", ModelOutput: "private attempt"}}}
		if i == 2 {
			l.ErrorCode = "upstream_timeout"
			l.Model = "model-b"
		}
		if err := s.Record(ctx, l, "", 30); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []struct {
		id, request, status string
		amount              any
		reserve             int64
	}{
		{"cost-1", "audit_search_0", "calculated", int64(1000000000), 0},
		{"cost-2", "audit_search_0", "pending", nil, 2000000000},
		{"cost-3", "audit_search_1", "estimated", int64(3000000000), 0},
	} {
		if _, err := s.DB.ExecContext(ctx, `INSERT INTO audit_costs(id,request_id,client_id,kind,policy_id,model,started_at,budget_date,reserved_pico,amount_pico,status) VALUES($1,$2,'client','production','policy','model-a',NOW(),CURRENT_DATE,$3,$4,$5)`, row.id, row.request, row.reserve, row.amount, row.status); err != nil {
			t.Fatal(err)
		}
	}
	items, total, err := s.Logs(ctx, LogFilter{Page: 1, PageSize: 20})
	if err != nil || total != 3 {
		t.Fatal(items, total, err)
	}
	for _, l := range items {
		if l.ModelOutput != "" || len(l.Attempts) != 1 || l.Attempts[0].ModelOutput != "" {
			t.Fatal("list exposed raw output", l)
		}
		want, err := s.requestCost(ctx, l.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(l.Cost, want) {
			t.Fatal("batch cost differs", l.Cost, want)
		}
	}
	for _, tc := range []struct {
		filter LogFilter
		count  int
	}{
		{LogFilter{RequestID: "audit_search_0"}, 1}, {LogFilter{RequestID: "' OR TRUE --"}, 0},
		{LogFilter{Model: "model-a"}, 2}, {LogFilter{ChannelID: "first"}, 3}, {LogFilter{ChannelID: "missing"}, 0},
		{LogFilter{ErrorCode: "upstream_timeout"}, 1},
	} {
		tc.filter.Page, tc.filter.PageSize = 1, 20
		_, n, err := s.Logs(ctx, tc.filter)
		if err != nil || n != tc.count {
			t.Fatal(tc.filter, n, err)
		}
	}
	detail, err := s.LogDetail(ctx, "audit_search_0")
	if err != nil || detail.ModelOutput != "private output" || detail.Attempts[0].ModelOutput != "private attempt" {
		t.Fatal("detail output lost", err)
	}
}
