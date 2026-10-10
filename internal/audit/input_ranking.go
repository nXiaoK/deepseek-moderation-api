package audit

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"net/http"
	"net/url"
	"strconv"
	"time"
	"unicode/utf8"
)

type InputRankingFilter struct {
	From, To                        time.Time
	PolicyID, ClientID, Fingerprint string
	Page, PageSize, MinCount        int
}

type InputRankingItem struct {
	Fingerprint     string    `json:"fingerprint"`
	Preview         *string   `json:"preview"`
	Input           *string   `json:"input,omitempty"`
	SampleRequestID string    `json:"sample_request_id,omitempty"`
	TextChars       int       `json:"text_chars"`
	Occurrences     int64     `json:"occurrences"`
	Flagged         int64     `json:"flagged"`
	KeywordBlocked  int64     `json:"keyword_blocked"`
	KeywordIgnored  int64     `json:"keyword_ignored"`
	ModelFlagged    int64     `json:"model_flagged"`
	ModelAllowed    int64     `json:"model_allowed"`
	Errors          int64     `json:"errors"`
	CacheHits       int64     `json:"cache_hits"`
	Clients         int64     `json:"clients"`
	FirstSeen       time.Time `json:"first_seen"`
	LastSeen        time.Time `json:"last_seen"`
}

type InputRankingSummary struct {
	IndexedRequests   int64      `json:"indexed_requests"`
	UniqueInputs      int64      `json:"unique_inputs"`
	RepeatedInputs    int64      `json:"repeated_inputs"`
	RepeatRequests    int64      `json:"repeat_requests"`
	UnindexedRequests int64      `json:"unindexed_requests"`
	BackfillPending   int64      `json:"backfill_pending"`
	OldestRetainedAt  *time.Time `json:"oldest_retained_at"`
}

type InputRankingResponse struct {
	From     time.Time           `json:"from"`
	To       time.Time           `json:"to"`
	PolicyID string              `json:"policy_id"`
	ClientID string              `json:"client_id"`
	Page     int                 `json:"page"`
	PageSize int                 `json:"page_size"`
	MinCount int                 `json:"min_count"`
	Total    int                 `json:"total"`
	Summary  InputRankingSummary `json:"summary"`
	Items    []InputRankingItem  `json:"items"`
}

func parseInputRankingFilter(q url.Values, now time.Time) (InputRankingFilter, error) {
	if q.Get("kind") != "" && q.Get("kind") != "production" || q.Get("model") != "" || q.Get("channel_id") != "" {
		return InputRankingFilter{}, problem(400, "invalid_filter", "输入排行仅统计正式请求，支持时间、策略和调用方筛选")
	}
	a, err := parseAnalyticsFilter(q, now)
	if err != nil {
		return InputRankingFilter{}, err
	}
	f := InputRankingFilter{From: a.From, To: a.To, PolicyID: a.PolicyID, ClientID: a.ClientID, Page: 1, PageSize: 20, MinCount: 2}
	for _, param := range []struct {
		key    string
		target *int
		max    int
	}{
		{"page", &f.Page, 1000000}, {"page_size", &f.PageSize, 100}, {"min_count", &f.MinCount, 1000000000},
	} {
		if value := q.Get(param.key); value != "" {
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 || n > param.max {
				return f, problem(400, "invalid_filter", "排行页码、每页条数或最低出现次数无效")
			}
			*param.target = n
		}
	}
	return f, nil
}

const inputRankingWhereSQL = `kind='production' AND expires_at>NOW() AND created_at >= $1 AND created_at < $2
 AND ($3='' OR policy_id=$3) AND ($4='' OR client_id=$4) AND ($5='' OR input_fingerprint=$5)`

const inputRankingBaseSQL = `WITH base AS (
 SELECT * FROM audit_requests WHERE ` + inputRankingWhereSQL + `
) `

const inputRankingGroupsSQL = inputRankingBaseSQL + `, grouped AS (
 SELECT input_fingerprint, COUNT(*) AS occurrences, COUNT(*) FILTER(WHERE flagged AND error_code='') AS flagged,
 COUNT(*) FILTER(WHERE error_code='' AND metadata->>'keyword_blocked'='true') AS keyword_blocked,
 COUNT(*) FILTER(WHERE error_code='' AND metadata->>'keyword_ignored'='true') AS keyword_ignored,
 COUNT(*) FILTER(WHERE error_code='' AND flagged AND metadata->>'keyword_blocked' IS DISTINCT FROM 'true') AS model_flagged,
 COUNT(*) FILTER(WHERE error_code='' AND NOT flagged AND metadata->>'keyword_ignored' IS DISTINCT FROM 'true') AS model_allowed,
 COUNT(*) FILTER(WHERE error_code<>'') AS errors,
 COUNT(*) FILTER(WHERE metadata->>'cache_hit'='true') AS cache_hits,
 COUNT(DISTINCT client_id) AS clients, MIN(created_at) AS first_seen, MAX(created_at) AS last_seen,
 COALESCE(MAX((metadata->'request'->>'text_chars')::integer),0) AS text_chars
 FROM base WHERE input_fingerprint IS NOT NULL GROUP BY input_fingerprint HAVING COUNT(*) >= $6
) `

func (s *Store) InputRanking(ctx context.Context, f InputRankingFilter, fullInput bool) (InputRankingResponse, error) {
	out := InputRankingResponse{From: f.From, To: f.To, PolicyID: f.PolicyID, ClientID: f.ClientID, Page: f.Page, PageSize: f.PageSize, MinCount: f.MinCount, Items: []InputRankingItem{}}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	args := []any{f.From, f.To, f.PolicyID, f.ClientID, f.Fingerprint}
	var oldest sql.NullTime
	err = tx.QueryRowContext(ctx, inputRankingBaseSQL+`SELECT
 COUNT(*) FILTER(WHERE input_fingerprint IS NOT NULL), COUNT(DISTINCT input_fingerprint),
 COUNT(*) FILTER(WHERE input_fingerprint IS NULL AND `+legacyRankingEligibleSQL+`),
 COUNT(*) FILTER(WHERE input_fingerprint IS NULL AND NOT input_fingerprint_checked AND octet_length(input_cipher)>0 AND `+legacyRankingEligibleSQL+`),
 MIN(created_at) FROM base`, args...).Scan(&out.Summary.IndexedRequests, &out.Summary.UniqueInputs, &out.Summary.UnindexedRequests, &out.Summary.BackfillPending, &oldest)
	if err != nil {
		return out, err
	}
	if oldest.Valid {
		out.Summary.OldestRetainedAt = &oldest.Time
	}
	out.Summary.RepeatRequests = out.Summary.IndexedRequests - out.Summary.UniqueInputs
	args = append(args, f.MinCount)
	err = tx.QueryRowContext(ctx, inputRankingBaseSQL+`SELECT COUNT(*) FILTER(WHERE occurrences >= $6), COUNT(*) FILTER(WHERE occurrences >= 2)
 FROM (SELECT COUNT(*) AS occurrences FROM base WHERE input_fingerprint IS NOT NULL GROUP BY input_fingerprint) g`, args...).Scan(&out.Total, &out.Summary.RepeatedInputs)
	if err != nil {
		return out, err
	}
	out.Page = min(f.Page, max(1, (out.Total+f.PageSize-1)/f.PageSize))
	if out.Total > 0 {
		if f.PageSize == 1000 && out.Total > 1000 {
			return out, problem(400, "export_too_large", "每次最多导出 1000 个输入，请缩小筛选范围或提高最低次数")
		}
		args = append(args, f.PageSize, (out.Page-1)*f.PageSize)
		rows, err := tx.QueryContext(ctx, inputRankingGroupsSQL+`SELECT g.input_fingerprint,g.occurrences,g.flagged,g.keyword_blocked,g.keyword_ignored,
 g.model_flagged,g.model_allowed,g.errors,g.cache_hits,g.clients,g.first_seen,g.last_seen,g.text_chars,COALESCE(sample.id,''),sample.input_cipher
 FROM (SELECT * FROM grouped ORDER BY occurrences DESC,last_seen DESC,input_fingerprint LIMIT $7 OFFSET $8) g
 LEFT JOIN LATERAL (SELECT id,input_cipher FROM audit_requests WHERE `+inputRankingWhereSQL+`
 AND input_fingerprint=g.input_fingerprint AND octet_length(input_cipher)>0 ORDER BY created_at DESC,id DESC LIMIT 1) sample ON TRUE
 ORDER BY g.occurrences DESC,g.last_seen DESC,g.input_fingerprint`, args...)
		if err != nil {
			return out, err
		}
		for rows.Next() {
			var item InputRankingItem
			var cipher []byte
			if err := rows.Scan(&item.Fingerprint, &item.Occurrences, &item.Flagged, &item.KeywordBlocked, &item.KeywordIgnored, &item.ModelFlagged, &item.ModelAllowed, &item.Errors, &item.CacheHits, &item.Clients, &item.FirstSeen, &item.LastSeen, &item.TextChars, &item.SampleRequestID, &cipher); err != nil {
				rows.Close()
				return out, err
			}
			if len(cipher) > 0 {
				text, err := s.Vault.Open(cipher, "input:"+item.SampleRequestID)
				if err != nil || s.inputFingerprint(text) != item.Fingerprint {
					rows.Close()
					return out, problem(503, "input_unavailable", "排行输入原文暂时无法读取")
				}
				item.TextChars = utf8.RuneCountInString(text)
				preview := text
				if item.TextChars > 120 {
					preview = string([]rune(text)[:120])
				}
				item.Preview = &preview
				if fullInput {
					item.Input = &text
				}
			}
			out.Items = append(out.Items, item)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return out, err
		}
	}
	return out, tx.Commit()
}

func (s *Server) inputRanking(w http.ResponseWriter, r *http.Request) error {
	f, err := parseInputRankingFilter(r.URL.Query(), time.Now())
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	out, err := s.Store.InputRanking(ctx, f, false)
	if err != nil {
		return err
	}
	return writeJSON(w, 200, out)
}

func (s *Server) inputRankingDetail(w http.ResponseWriter, r *http.Request) error {
	f, err := parseInputRankingFilter(r.URL.Query(), time.Now())
	if err != nil {
		return err
	}
	f.Fingerprint = r.PathValue("fingerprint")
	if !inputFingerprintPattern.MatchString(f.Fingerprint) {
		return problem(400, "invalid_filter", "输入指纹无效")
	}
	f.Page, f.PageSize, f.MinCount = 1, 1, 1
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	out, err := s.Store.InputRanking(ctx, f, true)
	if err != nil {
		return err
	}
	if len(out.Items) == 0 {
		return ErrNotFound
	}
	return writeJSON(w, 200, out.Items[0])
}

func (s *Server) exportInputRanking(w http.ResponseWriter, r *http.Request) error {
	f, err := parseInputRankingFilter(r.URL.Query(), time.Now())
	if err != nil {
		return err
	}
	f.Page, f.PageSize = 1, 1000
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	out, err := s.Store.InputRanking(ctx, f, false)
	if err != nil {
		return err
	}
	var buffer bytes.Buffer
	buffer.Write([]byte{0xef, 0xbb, 0xbf})
	csvOut := csv.NewWriter(&buffer)
	if err := csvOut.Write([]string{"排名", "输入摘要", "输入指纹", "原文字数", "出现次数", "拦截次数", "关键词阻止", "关键词忽略", "模型命中", "模型放行", "审核失败", "缓存命中", "调用方数量", "首次出现(北京时间)", "最近出现(北京时间)"}); err != nil {
		return err
	}
	for i, row := range out.Items {
		preview := "原文未保存"
		if row.Preview != nil {
			preview = *row.Preview
		}
		cells := []string{strconv.Itoa(i + 1), csvCell(preview), row.Fingerprint, strconv.Itoa(row.TextChars)}
		for _, n := range []int64{row.Occurrences, row.Flagged, row.KeywordBlocked, row.KeywordIgnored, row.ModelFlagged, row.ModelAllowed, row.Errors, row.CacheHits, row.Clients} {
			cells = append(cells, strconv.FormatInt(n, 10))
		}
		cells = append(cells, row.FirstSeen.In(shanghai).Format("2006-01-02 15:04:05"), row.LastSeen.In(shanghai).Format("2006-01-02 15:04:05"))
		if err := csvOut.Write(cells); err != nil {
			return err
		}
	}
	csvOut.Flush()
	if err := csvOut.Error(); err != nil {
		return err
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="input-ranking.csv"`)
	_, err = w.Write(buffer.Bytes())
	return err
}
