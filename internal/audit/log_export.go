package audit

import (
	"bytes"
	"encoding/csv"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func parseLogFilter(q url.Values) (LogFilter, error) {
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	if page > 1000000 {
		return LogFilter{}, problem(400, "invalid_page", "页码过大")
	}
	size, _ := strconv.Atoi(q.Get("page_size"))
	if size < 1 || size > 100 {
		size = 20
	}
	f := LogFilter{Page: page, PageSize: size, Kind: q.Get("kind"), PolicyID: q.Get("policy_id"), ClientID: q.Get("client_id"), Result: q.Get("result"), From: q.Get("from"), To: q.Get("to"), RequestID: strings.TrimSpace(q.Get("request_id")), Model: q.Get("model"), ChannelID: q.Get("channel_id"), ErrorCode: q.Get("error_code")}
	for _, value := range []string{f.RequestID, f.Model, f.ChannelID, f.ErrorCode, f.PolicyID, f.ClientID} {
		if len(value) > 200 {
			return f, problem(400, "invalid_filter", "筛选值过长")
		}
	}
	if f.Kind != "" && f.Kind != "production" && f.Kind != "test" {
		return f, problem(400, "invalid_filter", "请求来源无效")
	}
	if f.Result != "" && f.Result != "flagged" && f.Result != "allow" && f.Result != "error" && f.Result != "keyword_ignored" {
		return f, problem(400, "invalid_filter", "结果筛选无效")
	}
	var start, end time.Time
	var err error
	if f.From != "" {
		start, err = time.Parse(time.RFC3339, f.From)
		if err != nil {
			return f, problem(400, "invalid_date", "时间格式必须为 RFC3339")
		}
	}
	if f.To != "" {
		end, err = time.Parse(time.RFC3339, f.To)
		if err != nil {
			return f, problem(400, "invalid_date", "时间格式必须为 RFC3339")
		}
	}
	if !start.IsZero() && !end.IsZero() && !end.After(start) {
		return f, problem(400, "invalid_date", "结束时间必须晚于开始时间")
	}
	return f, nil
}
func (s *Server) exportLogs(w http.ResponseWriter, r *http.Request) error {
	f, err := parseLogFilter(r.URL.Query())
	if err != nil {
		return err
	}
	f.Page, f.PageSize = 1, 1000
	items, total, err := s.Store.Logs(r.Context(), f)
	if err != nil {
		return err
	}
	if total > 1000 {
		return problem(400, "export_too_large", "每次最多导出 1000 条，请缩小筛选范围")
	}
	var buffer bytes.Buffer
	buffer.Write([]byte{0xef, 0xbb, 0xbf})
	out := csv.NewWriter(&buffer)
	if err := out.Write([]string{"请求ID", "记录时间(北京时间)", "来源", "策略ID", "调用方ID", "模型", "通道ID", "判定", "评分", "调用次数", "耗时(ms)", "已知费用(CNY)", "费用状态", "错误码", "原因"}); err != nil {
		return err
	}
	for _, row := range items {
		decision, confidence, amount, costStatus := "未知", "", "", ""
		if row.ErrorCode != "" {
			decision = "失败"
		} else if row.KeywordIgnored {
			decision = "关键词忽略"
		} else if row.Confidence != nil {
			decision = "放行"
			if row.Flagged {
				decision = "命中"
			}
			confidence = strconv.FormatFloat(*row.Confidence, 'g', -1, 64)
		}
		if row.Cost != nil {
			costStatus = row.Cost.Status
			if row.Cost.AmountCNY != nil {
				amount = *row.Cost.AmountCNY
			}
		}
		reason := row.Reason
		if row.ErrorCode != "" {
			reason = row.ErrorMessage
		}
		cells := []string{row.ID, row.CreatedAt.In(shanghai).Format("2006-01-02 15:04:05"), row.Kind, row.PolicyID, row.ClientID, row.Model, row.ChannelID, decision, confidence, strconv.Itoa(row.AttemptCount), strconv.FormatInt(row.LatencyMS, 10), amount, costStatus, row.ErrorCode, reason}
		for i := range cells {
			cells[i] = csvCell(cells[i])
		}
		if err := out.Write(cells); err != nil {
			return err
		}
	}
	out.Flush()
	if err := out.Error(); err != nil {
		return err
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="audit-records.csv"`)
	_, err = w.Write(buffer.Bytes())
	return err
}
