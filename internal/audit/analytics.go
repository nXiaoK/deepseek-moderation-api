package audit

import (
	"context"
	"database/sql"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type AnalyticsFilter struct {
	From, To                      time.Time
	Kind, Model                   string
	PolicyID, ClientID, ChannelID string
	IntervalSeconds               int
}

type AnalyticsMetrics struct {
	SuccessfulCalls       int64    `json:"successful_calls"`
	FailedCalls           int64    `json:"failed_calls"`
	OutcomeSamples        int64    `json:"outcome_samples"`
	P50LatencyMS          *float64 `json:"p50_latency_ms"`
	P95LatencyMS          *float64 `json:"p95_latency_ms"`
	P99LatencyMS          *float64 `json:"p99_latency_ms"`
	Records               int64    `json:"records"`
	Calls                 int64    `json:"calls"`
	CacheHits             int64    `json:"cache_hits"`
	InputTokens           int64    `json:"input_tokens"`
	OutputTokens          int64    `json:"output_tokens"`
	TotalTokens           int64    `json:"total_tokens"`
	UnknownUsage          int64    `json:"unknown_usage"`
	KnownCostCNY          string   `json:"known_cost_cny"`
	EstimatedCostCNY      string   `json:"estimated_cost_cny"`
	ReservedCNY           string   `json:"reserved_cny"`
	PendingCosts          int64    `json:"pending_costs"`
	AvgLatencyMS          *float64 `json:"avg_latency_ms"`
	LatencySamples        int64    `json:"latency_samples"`
	OutputTokensPerSecond *float64 `json:"output_tokens_per_second"`
}
type AnalyticsModel struct {
	Model string `json:"model"`
	AnalyticsMetrics
}
type AnalyticsPoint struct {
	Time time.Time `json:"time"`
	AnalyticsMetrics
}
type AnalyticsResponse struct {
	PolicyID        string           `json:"policy_id"`
	ClientID        string           `json:"client_id"`
	ChannelID       string           `json:"channel_id"`
	Errors          []AnalyticsError `json:"errors"`
	Requests        RequestMetrics   `json:"requests"`
	From            time.Time        `json:"from"`
	To              time.Time        `json:"to"`
	Kind            string           `json:"kind"`
	Model           string           `json:"model"`
	IntervalSeconds int              `json:"interval_seconds"`
	AvailableModels []string         `json:"available_models"`
	Summary         AnalyticsMetrics `json:"summary"`
	Models          []AnalyticsModel `json:"models"`
	Series          []AnalyticsPoint `json:"series"`
}
type AnalyticsError struct {
	Code  string `json:"code"`
	Calls int64  `json:"calls"`
}
type RequestMetrics struct {
	Records      int64    `json:"records"`
	Allowed      int64    `json:"allowed"`
	Flagged      int64    `json:"flagged"`
	Errors       int64    `json:"errors"`
	Fallbacks    int64    `json:"fallbacks"`
	P50LatencyMS *float64 `json:"p50_latency_ms"`
	P95LatencyMS *float64 `json:"p95_latency_ms"`
	P99LatencyMS *float64 `json:"p99_latency_ms"`
}

func parseAnalyticsFilter(q url.Values, now time.Time) (AnalyticsFilter, error) {
	f := AnalyticsFilter{To: now.UTC(), Kind: q.Get("kind"), Model: strings.TrimSpace(q.Get("model"))}
	f.PolicyID, f.ClientID, f.ChannelID = q.Get("policy_id"), q.Get("client_id"), q.Get("channel_id")
	for _, value := range []string{f.PolicyID, f.ClientID, f.ChannelID} {
		if len(value) > 200 {
			return f, problem(400, "invalid_filter", "筛选值过长")
		}
	}
	if f.Kind == "" {
		f.Kind = "production"
	}
	if f.Kind != "production" && f.Kind != "test" && f.Kind != "all" {
		return f, problem(400, "invalid_kind", "来源必须为正式请求、后台试跑或全部")
	}
	if len(f.Model) > 200 {
		return f, problem(400, "invalid_model", "模型名称过长")
	}
	rangeName := q.Get("range")
	if rangeName == "" {
		rangeName = "24h"
	}
	durations := map[string]time.Duration{"1h": time.Hour, "24h": 24 * time.Hour, "48h": 48 * time.Hour, "7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour}
	if rangeName == "custom" {
		var err error
		f.From, err = time.Parse(time.RFC3339, q.Get("from"))
		if err != nil {
			return f, problem(400, "invalid_date", "自定义开始时间必须包含时区")
		}
		f.To, err = time.Parse(time.RFC3339, q.Get("to"))
		if err != nil {
			return f, problem(400, "invalid_date", "自定义结束时间必须包含时区")
		}
	} else {
		d, ok := durations[rangeName]
		if !ok {
			return f, problem(400, "invalid_range", "不支持的统计时间范围")
		}
		f.From = f.To.Add(-d)
	}
	span := f.To.Sub(f.From)
	if span <= 0 || span > 366*24*time.Hour {
		return f, problem(400, "invalid_range", "结束时间须晚于开始时间，范围最多 366 天")
	}
	f.IntervalSeconds = 86400
	if span <= time.Hour {
		f.IntervalSeconds = 60
	} else if span <= 48*time.Hour {
		f.IntervalSeconds = 3600
	}
	return f, nil
}

func (s *Server) analytics(w http.ResponseWriter, r *http.Request) error {
	f, err := parseAnalyticsFilter(r.URL.Query(), time.Now())
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	data, err := s.Store.Analytics(ctx, f)
	if err != nil {
		return err
	}
	return writeJSON(w, 200, data)
}

// Cost rows are per attempt, so retries belong to the model that incurred
// them. Current accounting values also reflect manual cost reconciliation.
// Older rows recover timing from retained request metadata when available.
const analyticsBaseSQL = `WITH base AS (
 SELECT COALESCE(NULLIF(c.usage->>'actual_model',''),c.model) AS effective_model,
 date_bin($4 * INTERVAL '1 second',c.started_at,TIMESTAMPTZ '2000-01-01 00:00:00+08') AS bucket,
 c.status,c.amount_pico,c.reserved_pico,c.usage,
 (c.outcome_known OR a.item IS NOT NULL) AS outcome_known,
 CASE WHEN c.outcome_known THEN c.error_code ELSE COALESCE(a.item->>'error_code','') END AS error_code,
 COALESCE(c.request_sent,(a.item->>'sent')::boolean,c.status NOT IN ('local_cache','zero','reserved')) AS sent,
 COALESCE(c.latency_ms,CASE WHEN (a.item->>'sent')::boolean THEN (a.item->>'latency_ms')::bigint END) AS latency
 FROM audit_costs c
 LEFT JOIN audit_requests r ON ((c.latency_ms IS NULL AND c.request_sent IS NULL) OR NOT c.outcome_known) AND r.id=c.request_id
 LEFT JOIN LATERAL (
  SELECT item FROM jsonb_array_elements(CASE WHEN jsonb_typeof(r.metadata->'attempts')='array' THEN r.metadata->'attempts' ELSE '[]'::jsonb END) item
  WHERE item->>'id'=c.id LIMIT 1
 ) a ON TRUE
 WHERE c.started_at >= $1 AND c.started_at < $2 AND ($3='all' OR c.kind=$3)
 AND ($6='' OR c.policy_id=$6) AND ($7='' OR c.client_id=$7) AND ($8='' OR c.channel_id=$8)
), eligible AS (
 SELECT *,COALESCE((usage->>'reported')::boolean,FALSE) AS reported FROM base
 WHERE ($5='' OR effective_model=$5)
)
`
const analyticsSQL = analyticsBaseSQL + `
SELECT GROUPING(effective_model),GROUPING(bucket),COALESCE(effective_model,''),bucket,
 COUNT(*),COUNT(*) FILTER(WHERE sent),COUNT(*) FILTER(WHERE status='local_cache'),
 COALESCE(SUM((usage->>'prompt_tokens')::bigint) FILTER(WHERE sent AND reported),0),
 COALESCE(SUM((usage->>'completion_tokens')::bigint) FILTER(WHERE sent AND reported),0),
 COALESCE(SUM((usage->>'total_tokens')::bigint) FILTER(WHERE sent AND reported),0),
 COUNT(*) FILTER(WHERE sent AND NOT reported),
 COALESCE(SUM(amount_pico),0),COALESCE(SUM(amount_pico) FILTER(WHERE status='estimated'),0),
 COALESCE(SUM(reserved_pico) FILTER(WHERE amount_pico IS NULL),0),COUNT(*) FILTER(WHERE amount_pico IS NULL),
 (AVG(latency) FILTER(WHERE sent AND latency IS NOT NULL))::double precision,
 COUNT(*) FILTER(WHERE sent AND latency IS NOT NULL),
 (SUM((usage->>'completion_tokens')::bigint) FILTER(WHERE sent AND reported AND latency>0))::double precision
 / NULLIF((SUM(latency) FILTER(WHERE sent AND reported AND latency>0))::double precision/1000,0),
 COUNT(*) FILTER(WHERE sent AND outcome_known AND error_code=''),
 COUNT(*) FILTER(WHERE sent AND outcome_known AND error_code<>''),
 COUNT(*) FILTER(WHERE sent AND outcome_known),
 percentile_cont(0.5) WITHIN GROUP(ORDER BY latency) FILTER(WHERE sent AND latency IS NOT NULL),
 percentile_cont(0.95) WITHIN GROUP(ORDER BY latency) FILTER(WHERE sent AND latency IS NOT NULL),
 percentile_cont(0.99) WITHIN GROUP(ORDER BY latency) FILTER(WHERE sent AND latency IS NOT NULL)
FROM eligible GROUP BY GROUPING SETS ((),(effective_model),(bucket)) ORDER BY effective_model,bucket`

func (s *Store) Analytics(ctx context.Context, f AnalyticsFilter) (AnalyticsResponse, error) {
	out := AnalyticsResponse{From: f.From, To: f.To, Kind: f.Kind, Model: f.Model, IntervalSeconds: f.IntervalSeconds, AvailableModels: []string{}, Models: []AnalyticsModel{}, Series: []AnalyticsPoint{}}
	out.PolicyID, out.ClientID, out.ChannelID = f.PolicyID, f.ClientID, f.ChannelID
	out.Errors = []AnalyticsError{}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	options, err := tx.QueryContext(ctx, `SELECT DISTINCT COALESCE(NULLIF(usage->>'actual_model',''),model) FROM audit_costs WHERE started_at >= $1 AND started_at < $2 AND ($3='all' OR kind=$3) AND ($4='' OR policy_id=$4) AND ($5='' OR client_id=$5) AND ($6='' OR channel_id=$6) ORDER BY 1`, f.From, f.To, f.Kind, f.PolicyID, f.ClientID, f.ChannelID)
	if err != nil {
		return out, err
	}
	for options.Next() {
		var model string
		if err = options.Scan(&model); err != nil {
			options.Close()
			return out, err
		}
		out.AvailableModels = append(out.AvailableModels, model)
	}
	err = options.Err()
	options.Close()
	if err != nil {
		return out, err
	}
	args := []any{f.From, f.To, f.Kind, f.IntervalSeconds, f.Model, f.PolicyID, f.ClientID, f.ChannelID}
	rows, err := tx.QueryContext(ctx, analyticsSQL, args...)
	if err != nil {
		return out, err
	}
	points := map[int64]AnalyticsMetrics{}
	for rows.Next() {
		var gm, gb int
		var model string
		var bucket sql.NullTime
		var m AnalyticsMetrics
		var known, estimated, reserved int64
		var latency, speed sql.NullFloat64
		var p50, p95, p99 sql.NullFloat64
		err = rows.Scan(&gm, &gb, &model, &bucket, &m.Records, &m.Calls, &m.CacheHits, &m.InputTokens, &m.OutputTokens, &m.TotalTokens, &m.UnknownUsage, &known, &estimated, &reserved, &m.PendingCosts, &latency, &m.LatencySamples, &speed, &m.SuccessfulCalls, &m.FailedCalls, &m.OutcomeSamples, &p50, &p95, &p99)
		if err != nil {
			rows.Close()
			return out, err
		}
		m.KnownCostCNY, m.EstimatedCostCNY, m.ReservedCNY = picoString(known), picoString(estimated), picoString(reserved)
		if latency.Valid {
			m.AvgLatencyMS = &latency.Float64
		}
		if speed.Valid {
			m.OutputTokensPerSecond = &speed.Float64
		}
		m.P50LatencyMS, m.P95LatencyMS, m.P99LatencyMS = analyticsFloat(p50), analyticsFloat(p95), analyticsFloat(p99)
		switch {
		case gm == 1 && gb == 1:
			out.Summary = m
		case gm == 0:
			out.Models = append(out.Models, AnalyticsModel{model, m})
		case bucket.Valid:
			points[bucket.Time.Unix()] = m
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	issues, err := tx.QueryContext(ctx, analyticsBaseSQL+` SELECT error_code,COUNT(*) FROM eligible WHERE sent AND outcome_known AND error_code<>'' GROUP BY error_code ORDER BY COUNT(*) DESC,error_code LIMIT 20`, args...)
	if err != nil {
		return out, err
	}
	for issues.Next() {
		var item AnalyticsError
		if err := issues.Scan(&item.Code, &item.Calls); err != nil {
			issues.Close()
			return out, err
		}
		out.Errors = append(out.Errors, item)
	}
	err = issues.Err()
	issues.Close()
	if err != nil {
		return out, err
	}
	var p50, p95, p99 sql.NullFloat64
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COUNT(*) FILTER(WHERE r.error_code='' AND NOT r.flagged),COUNT(*) FILTER(WHERE r.flagged),COUNT(*) FILTER(WHERE r.error_code<>''),COUNT(*) FILTER(WHERE (r.metadata->>'attempt_count')::int>1),percentile_cont(0.5) WITHIN GROUP(ORDER BY (r.metadata->>'latency_ms')::bigint),percentile_cont(0.95) WITHIN GROUP(ORDER BY (r.metadata->>'latency_ms')::bigint),percentile_cont(0.99) WITHIN GROUP(ORDER BY (r.metadata->>'latency_ms')::bigint)
 FROM audit_requests r WHERE r.created_at>=$1 AND r.created_at<$2 AND r.expires_at>NOW() AND ($3='all' OR r.kind=$3) AND ($4='' OR r.policy_id=$4) AND ($5='' OR r.client_id=$5)
 AND (($6='' AND $7='') OR EXISTS(SELECT 1 FROM audit_costs c WHERE c.request_id=r.id AND ($6='' OR COALESCE(NULLIF(c.usage->>'actual_model',''),c.model)=$6) AND ($7='' OR c.channel_id=$7)))`, f.From, f.To, f.Kind, f.PolicyID, f.ClientID, f.Model, f.ChannelID).Scan(&out.Requests.Records, &out.Requests.Allowed, &out.Requests.Flagged, &out.Requests.Errors, &out.Requests.Fallbacks, &p50, &p95, &p99)
	if err != nil {
		return out, err
	}
	out.Requests.P50LatencyMS, out.Requests.P95LatencyMS, out.Requests.P99LatencyMS = analyticsFloat(p50), analyticsFloat(p95), analyticsFloat(p99)
	if err = tx.Commit(); err != nil {
		return out, err
	}
	// Align buckets to Beijing time and include empty intervals for honest plots.
	seconds := int64(f.IntervalSeconds)
	start := f.From.UTC().Add(8 * time.Hour).Truncate(time.Duration(seconds) * time.Second).Add(-8 * time.Hour)
	for at := start; at.Before(f.To); at = at.Add(time.Duration(seconds) * time.Second) {
		m, ok := points[at.Unix()]
		if !ok {
			m.KnownCostCNY, m.EstimatedCostCNY, m.ReservedCNY = "0", "0", "0"
		}
		out.Series = append(out.Series, AnalyticsPoint{at, m})
	}
	return out, nil
}
func analyticsFloat(value sql.NullFloat64) *float64 {
	if !value.Valid {
		return nil
	}
	return &value.Float64
}
