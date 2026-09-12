package audit

import (
	"encoding/json"
	"errors"
	"math"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

type PolicyConfig struct {
	ResultCacheTTL int     `json:"result_cache_ttl_seconds"`
	Prompt         string  `json:"prompt"`
	Threshold      float64 `json:"threshold"`
	Model          string  `json:"model"`
	BaseURL        string  `json:"base_url"`
	CredentialID   string  `json:"credential_id"`
	TimeoutMS      int     `json:"timeout_ms"`
	MaxTokens      int     `json:"max_tokens"`
	StoreInput     bool    `json:"store_input"`
	RetentionDays  int     `json:"retention_days"`
}

func (c PolicyConfig) Validate() error {
	if c.ResultCacheTTL < 0 || c.ResultCacheTTL > 3600 {
		return errors.New("结果缓存时间必须为 0～3600 秒；0 表示关闭")
	}
	if strings.TrimSpace(c.Prompt) == "" || utf8.RuneCountInString(c.Prompt) > 64000 {
		return errors.New("提示词必须为 1～64000 个字符")
	}
	if math.IsNaN(c.Threshold) || math.IsInf(c.Threshold, 0) || c.Threshold < 0 || c.Threshold > 1 {
		return errors.New("阈值必须在 0～1 之间")
	}
	if !regexp.MustCompile(`^[a-zA-Z0-9._:/-]{1,100}$`).MatchString(c.Model) {
		return errors.New("模型名称无效")
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil || u.Scheme != "https" || u.Host != "api.deepseek.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/" && u.Path != "/v1" && u.Path != "/v1/") {
		return errors.New("上游地址必须为 https://api.deepseek.com 或其 /v1 路径")
	}
	if c.TimeoutMS < 1000 || c.TimeoutMS > 30000 {
		return errors.New("超时必须为 1000～30000 ms")
	}
	if c.MaxTokens < 64 || c.MaxTokens > 4096 {
		return errors.New("输出上限必须为 64～4096 tokens")
	}
	if c.RetentionDays < 1 || c.RetentionDays > 365 {
		return errors.New("记录保留时间必须为 1～365 天")
	}
	return nil
}

type Policy struct {
	ID            string        `json:"id"`
	Name          string        `json:"name"`
	Alias         string        `json:"alias"`
	Enabled       bool          `json:"enabled"`
	DraftRevision int64         `json:"draft_revision"`
	ActiveVersion int           `json:"active_version"`
	Draft         PolicyConfig  `json:"draft"`
	Active        *PolicyConfig `json:"active,omitempty"`
}
type Version struct {
	Version   int          `json:"version"`
	Config    PolicyConfig `json:"config"`
	CreatedAt time.Time    `json:"created_at"`
	Author    string       `json:"author"`
}
type Credential struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Masked string `json:"masked"`
	Active bool   `json:"active"`
}
type ClientKey struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Prefix    string    `json:"prefix"`
	PolicyIDs []string  `json:"policy_ids"`
	RPM       int       `json:"rpm"`
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"created_at"`
}
type Assessment struct {
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
}
type AuditMetadata struct {
	SchemaVersion int     `json:"schema_version"`
	PolicyID      string  `json:"policy_id"`
	PolicyVersion int     `json:"policy_version"`
	Confidence    float64 `json:"confidence"`
	Threshold     float64 `json:"threshold"`
	Reason        string  `json:"reason"`
}
type Result struct {
	Flagged    bool               `json:"flagged"`
	Categories map[string]bool    `json:"categories"`
	Scores     map[string]float64 `json:"category_scores"`
	Audit      AuditMetadata      `json:"audit"`
}
type Usage struct {
	PromptTokens     int  `json:"prompt_tokens"`
	CompletionTokens int  `json:"completion_tokens"`
	TotalTokens      int  `json:"total_tokens"`
	CacheHitTokens   *int `json:"prompt_cache_hit_tokens,omitempty"`
	CacheMissTokens  *int `json:"prompt_cache_miss_tokens,omitempty"`
	ReasoningTokens  int  `json:"reasoning_tokens,omitempty"`
	Reported         bool `json:"reported"`
	Attempted        bool `json:"-"`
}
type Response struct {
	CacheHit  bool      `json:"cache_hit"`
	Cost      *CostView `json:"cost,omitempty"`
	ID        string    `json:"id"`
	Model     string    `json:"model"`
	Results   []Result  `json:"results"`
	Usage     Usage     `json:"usage"`
	LatencyMS int64     `json:"latency_ms"`
}
type AuditLog struct {
	CacheHit      bool      `json:"cache_hit"`
	Cost          *CostView `json:"cost,omitempty"`
	ID            string    `json:"id"`
	Kind          string    `json:"kind"`
	PolicyID      string    `json:"policy_id"`
	PolicyVersion int       `json:"policy_version"`
	ClientID      string    `json:"client_id"`
	Model         string    `json:"model"`
	Flagged       bool      `json:"flagged"`
	Confidence    *float64  `json:"confidence"`
	Threshold     float64   `json:"threshold"`
	Reason        string    `json:"reason"`
	ErrorCode     string    `json:"error_code"`
	LatencyMS     int64     `json:"latency_ms"`
	Usage         Usage     `json:"usage"`
	InputStored   bool      `json:"input_stored"`
	Input         string    `json:"input,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}
type LogFilter struct {
	Page, PageSize                             int
	Kind, PolicyID, ClientID, Result, From, To string
}
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string                    { return e.Message }
func problem(status int, code, message string) error { return &APIError{status, code, message} }

var ErrConflict = &APIError{409, "revision_conflict", "配置已被更新，请重新加载后保存"}
var ErrNotFound = &APIError{404, "not_found", "记录不存在"}

func parseText(raw json.RawMessage) (string, error) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return validateText(text)
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if len(raw) == 0 || raw[0] != '[' || strictJSON(raw, &parts) != nil || len(parts) == 0 {
		return "", problem(400, "invalid_input", "input 必须为文本或文本内容块数组")
	}
	var chunks []string
	for _, part := range parts {
		if part.Type != "text" {
			return "", problem(400, "unsupported_input", "当前策略只支持文本审核，不能忽略图片或其他内容")
		}
		chunks = append(chunks, part.Text)
	}
	return validateText(strings.Join(chunks, "\n"))
}
func validateText(s string) (string, error) {
	if strings.TrimSpace(s) == "" {
		return "", problem(400, "empty_input", "审核文本不能为空")
	}
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > 64000 {
		return "", problem(413, "input_too_large", "审核文本最多 64000 个字符")
	}
	return s, nil
}
