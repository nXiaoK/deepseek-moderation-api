package audit

import (
	"errors"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

var modelNamePattern = regexp.MustCompile(`^[a-zA-Z0-9._:/~-]{1,100}$`)
var policyAliasPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,79}$`)

type PolicyConfig struct {
	APIFormat          string  `json:"api_format,omitempty"`
	TextOnly           bool    `json:"text_only,omitempty"`
	ImageCount         int     `json:"-"`
	Provider           string  `json:"provider,omitempty"`
	ConnectionRevision string  `json:"connection_revision,omitempty"`
	ResultCacheTTL     int     `json:"result_cache_ttl_seconds"`
	Prompt             string  `json:"prompt"`
	Threshold          float64 `json:"threshold"`
	Model              string  `json:"model"`
	BaseURL            string  `json:"base_url"`
	CredentialID       string  `json:"credential_id"`
	TimeoutMS          int     `json:"timeout_ms"`
	MaxTokens          int     `json:"max_tokens"`
	StoreInput         bool    `json:"store_input"`
	RetentionDays      int     `json:"retention_days"`
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
	if !modelNamePattern.MatchString(c.Model) {
		return errors.New("模型名称须为 1～100 个字符，支持字母、数字及 . _ : / ~ -")
	}
	if err := validateProviderURL(c.ProviderID(), c.BaseURL); err != nil {
		return err
	}
	if err := validateAPIFormat(c.APIFormat); err != nil {
		return err
	}
	if len(c.ConnectionRevision) > 100 {
		return errors.New("连接修订最多 100 字节")
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

// Policy stores only the current settings. Revision is an optimistic lock and
// the policy_version included in our optional audit metadata.
type Policy struct {
	Archived  bool           `json:"archived"`
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Alias     string         `json:"alias"`
	Enabled   bool           `json:"enabled"`
	Revision  int64          `json:"revision"`
	Config    PolicySettings `json:"config"`
	UpdatedAt time.Time      `json:"updated_at"`
}
type ChannelBinding struct {
	ChannelID string `json:"channel_id"`
	Priority  int    `json:"priority"`
	Weight    int    `json:"weight"`
	Enabled   bool   `json:"enabled"`
}
type PolicySettings struct {
	KeywordIgnoreEnabled     bool             `json:"keyword_ignore_enabled"`
	IgnoreKeywords           []string         `json:"ignore_keywords,omitempty"`
	StoreModelOutput         *bool            `json:"store_model_output,omitempty"`
	ModelOutputRetentionDays int              `json:"model_output_retention_days,omitempty"`
	Prompt                   string           `json:"prompt"`
	Threshold                float64          `json:"threshold"`
	ResultCacheTTL           int              `json:"result_cache_ttl_seconds"`
	StoreInput               bool             `json:"store_input"`
	RetentionDays            int              `json:"retention_days"`
	TotalTimeoutMS           int              `json:"total_timeout_ms"`
	MaxAttempts              int              `json:"max_attempts"`
	FailureThreshold         int              `json:"failure_threshold"`
	FailureCooldownMinutes   int              `json:"failure_cooldown_minutes"`
	Channels                 []ChannelBinding `json:"channels"`
}

func DefaultSettings() PolicySettings {
	storeOutput := false
	return PolicySettings{StoreModelOutput: &storeOutput, ModelOutputRetentionDays: 7, Prompt: InitialPrompt, Threshold: .8, RetentionDays: 30, TotalTimeoutMS: 9000, MaxAttempts: 3, FailureThreshold: 3, FailureCooldownMinutes: 30, Channels: []ChannelBinding{}}
}

// Zero preserves compatibility with policy JSON saved before these settings existed.
func (c PolicySettings) failureThreshold() int {
	if c.FailureThreshold == 0 {
		return 3
	}
	return c.FailureThreshold
}
func (c PolicySettings) failureCooldown() time.Duration {
	minutes := c.FailureCooldownMinutes
	if minutes == 0 {
		minutes = 30
	}
	return time.Duration(minutes) * time.Minute
}
func (c PolicySettings) retainModelOutput() bool {
	return c.StoreModelOutput == nil || *c.StoreModelOutput
}
func (c PolicySettings) Validate() error {
	if len(c.IgnoreKeywords) > 100 {
		return errors.New("忽略关键词最多 100 条")
	}
	total := 0
	for _, keyword := range c.IgnoreKeywords {
		size := utf8.RuneCountInString(keyword)
		if strings.TrimSpace(keyword) == "" || size > 1000 {
			return errors.New("忽略关键词不能为空或纯空白，每条最多 1000 个字符")
		}
		total += size
	}
	if total > 16000 {
		return errors.New("忽略关键词合计最多 16000 个字符")
	}
	if c.ModelOutputRetentionDays < 0 || c.ModelOutputRetentionDays > 365 {
		return errors.New("模型输出保留时间必须为 1～365 天")
	}
	if c.Channels == nil {
		return errors.New("channels 必须为数组，可为空数组")
	}
	cfg := DefaultConfig()
	cfg.Prompt, cfg.Threshold, cfg.ResultCacheTTL = c.Prompt, c.Threshold, c.ResultCacheTTL
	cfg.StoreInput, cfg.RetentionDays = c.StoreInput, c.RetentionDays
	if err := cfg.Validate(); err != nil {
		return err
	}
	if c.TotalTimeoutMS < 1000 || c.TotalTimeoutMS > 25000 {
		return errors.New("总调用时限必须为 1000～25000 ms")
	}
	if c.MaxAttempts < 1 || c.MaxAttempts > 5 {
		return errors.New("最多调用次数必须为 1～5 次，包含首次调用")
	}
	if c.FailureThreshold < 0 || c.FailureThreshold > 100 {
		return errors.New("连续失败次数必须为 1～100 次（0 使用默认 3 次）")
	}
	if c.FailureCooldownMinutes < 0 || c.FailureCooldownMinutes > 1440 {
		return errors.New("失败冷却时间必须为 1～1440 分钟（0 使用默认 30 分钟）")
	}
	if len(c.Channels) > 100 {
		return errors.New("每个策略最多绑定 100 个模型通道")
	}
	seen := map[string]bool{}
	for _, b := range c.Channels {
		if b.ChannelID == "" || seen[b.ChannelID] || b.Priority < 1 || b.Priority > 100 || b.Weight < 1 || b.Weight > 1000 {
			return errors.New("通道不能重复，优先级必须为 1～100，权重必须为 1～1000")
		}
		seen[b.ChannelID] = true
	}
	return nil
}

type ModelChannel struct {
	APIFormat        string        `json:"api_format"`
	ID               string        `json:"id"`
	Name             string        `json:"name"`
	Provider         string        `json:"provider"`
	BaseURL          string        `json:"base_url"`
	Model            string        `json:"model"`
	CredentialID     string        `json:"credential_id"`
	CredentialActive bool          `json:"credential_active"`
	TimeoutMS        int           `json:"timeout_ms"`
	MaxTokens        int           `json:"max_tokens"`
	MaxConcurrency   int           `json:"max_concurrency"`
	RPM              int           `json:"rpm"`
	TextOnly         bool          `json:"text_only"`
	Enabled          bool          `json:"enabled"`
	Revision         int64         `json:"revision"`
	CacheEpoch       string        `json:"-"`
	PolicyNames      []string      `json:"policy_names"`
	Health           ChannelHealth `json:"health"`
}

func (c ModelChannel) Inference(r PolicySettings) PolicyConfig {
	return PolicyConfig{APIFormat: c.APIFormat, TextOnly: c.TextOnly, Provider: c.Provider, Model: c.Model, BaseURL: c.BaseURL, CredentialID: c.CredentialID, TimeoutMS: c.TimeoutMS, MaxTokens: c.MaxTokens, Prompt: r.Prompt, Threshold: r.Threshold, ResultCacheTTL: r.ResultCacheTTL, StoreInput: r.StoreInput, RetentionDays: r.RetentionDays, ConnectionRevision: c.CacheEpoch}
}

type AuditAttempt struct {
	InputScope   string    `json:"input_scope,omitempty"`
	ImageCount   int       `json:"image_count,omitempty"`
	ErrorMessage string    `json:"error_message,omitempty"`
	ModelOutput  string    `json:"model_output,omitempty"`
	ID           string    `json:"id"`
	ChannelID    string    `json:"channel_id"`
	ChannelName  string    `json:"channel_name"`
	Provider     string    `json:"provider"`
	Model        string    `json:"model"`
	ErrorCode    string    `json:"error_code"`
	LatencyMS    int64     `json:"latency_ms"`
	CacheHit     bool      `json:"cache_hit"`
	Sent         bool      `json:"sent"`
	Usage        Usage     `json:"usage"`
	Cost         *CostView `json:"cost,omitempty"`
}
type Credential struct {
	APIFormat string `json:"api_format"`
	Provider  string `json:"provider"`
	BaseURL   string `json:"base_url"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	Masked    string `json:"masked"`
	Active    bool   `json:"active"`
}
type ClientKey struct {
	Revision   int64      `json:"revision"`
	ExpiresAt  *time.Time `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	RotatedAt  *time.Time `json:"rotated_at"`
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	PolicyIDs  []string   `json:"policy_ids"`
	RPM        int        `json:"rpm"`
	Active     bool       `json:"active"`
	CreatedAt  time.Time  `json:"created_at"`
}
type Assessment struct {
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
}
type AuditMetadata struct {
	KeywordIgnored bool    `json:"keyword_ignored,omitempty"`
	SchemaVersion  int     `json:"schema_version"`
	PolicyID       string  `json:"policy_id"`
	PolicyVersion  int     `json:"policy_version"`
	Confidence     float64 `json:"confidence"`
	Threshold      float64 `json:"threshold"`
	Reason         string  `json:"reason"`
}
type Result struct {
	Flagged    bool               `json:"flagged"`
	Categories map[string]bool    `json:"categories"`
	Scores     map[string]float64 `json:"category_scores"`
	Audit      AuditMetadata      `json:"audit"`
}
type Usage struct {
	UpstreamRequestID string `json:"upstream_request_id,omitempty"`
	ActualModel       string `json:"actual_model,omitempty"`
	PromptTokens      int    `json:"prompt_tokens"`
	CompletionTokens  int    `json:"completion_tokens"`
	TotalTokens       int    `json:"total_tokens"`
	CacheHitTokens    *int   `json:"prompt_cache_hit_tokens,omitempty"`
	CacheMissTokens   *int   `json:"prompt_cache_miss_tokens,omitempty"`
	ReasoningTokens   int    `json:"reasoning_tokens,omitempty"`
	Reported          bool   `json:"reported"`
	Attempted         bool   `json:"-"`
}
type Response struct {
	InputScope   string         `json:"input_scope,omitempty"`
	ChannelID    string         `json:"channel_id"`
	ActualModel  string         `json:"actual_model"`
	AttemptCount int            `json:"attempt_count"`
	Attempts     []AuditAttempt `json:"attempts,omitempty"`
	Provider     string         `json:"provider"`
	CacheHit     bool           `json:"cache_hit"`
	Cost         *CostView      `json:"cost,omitempty"`
	ID           string         `json:"id"`
	Model        string         `json:"model"`
	Results      []Result       `json:"results"`
	Usage        Usage          `json:"usage"`
	LatencyMS    int64          `json:"latency_ms"`
}
type AuditLog struct {
	KeywordIgnored           bool           `json:"keyword_ignored,omitempty"`
	ModelOutputStored        bool           `json:"model_output_stored"`
	ModelOutputRetentionDays int            `json:"-"`
	Request                  *AuditRequest  `json:"request,omitempty"`
	ErrorMessage             string         `json:"error_message,omitempty"`
	ChannelID                string         `json:"channel_id"`
	Attempts                 []AuditAttempt `json:"attempts"`
	AttemptCount             int            `json:"attempt_count"`
	Provider                 string         `json:"provider"`
	UpstreamRequestID        string         `json:"upstream_request_id,omitempty"`
	CacheHit                 bool           `json:"cache_hit"`
	Cost                     *CostView      `json:"cost,omitempty"`
	ID                       string         `json:"id"`
	Kind                     string         `json:"kind"`
	PolicyID                 string         `json:"policy_id"`
	ClientID                 string         `json:"client_id"`
	Model                    string         `json:"model"`
	Flagged                  bool           `json:"flagged"`
	Confidence               *float64       `json:"confidence"`
	Threshold                float64        `json:"threshold"`
	Reason                   string         `json:"reason"`
	ModelOutput              string         `json:"model_output,omitempty"`
	ErrorCode                string         `json:"error_code"`
	LatencyMS                int64          `json:"latency_ms"`
	Usage                    Usage          `json:"usage"`
	InputStored              bool           `json:"input_stored"`
	Input                    string         `json:"input,omitempty"`
	CreatedAt                time.Time      `json:"created_at"`
}
type LogFilter struct {
	KeywordIgnore                              string
	LatencyGTMS                                *int64
	Page, PageSize                             int
	Kind, PolicyID, ClientID, Result, From, To string
	RequestID, Model, ChannelID, ErrorCode     string
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

func validateText(s string) (string, error) {
	if strings.TrimSpace(s) == "" {
		return "", problem(400, "empty_input", "审核文本不能为空")
	}
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > 64000 {
		return "", problem(413, "input_too_large", "审核文本最多 64000 个字符")
	}
	return s, nil
}
