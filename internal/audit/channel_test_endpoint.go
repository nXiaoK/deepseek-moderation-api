package audit

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"
)

const channelTestInput = "你好，这是一条模型连接测试消息。"
const channelTestPrompt = `判断用户输入是否包含违法或滥用内容。仅返回 JSON 对象，包含 confidence（0 到 1，表示违规置信度）和 reason（不超过 80 个字符的简短原因），不要返回其他字段或 Markdown。`

type upstreamStatusKey struct{}

func recordUpstreamStatus(ctx context.Context, status int) {
	if target, ok := ctx.Value(upstreamStatusKey{}).(*int); ok {
		*target = status
	}
}

type ChannelTestResult struct {
	OK           bool        `json:"ok"`
	Attempted    bool        `json:"attempted"`
	ChannelID    string      `json:"channel_id"`
	ChannelName  string      `json:"channel_name"`
	Model        string      `json:"model"`
	APIFormat    string      `json:"api_format"`
	Endpoint     string      `json:"endpoint"`
	TimeoutMS    int         `json:"timeout_ms"`
	MaxTokens    int         `json:"max_tokens"`
	LatencyMS    int64       `json:"latency_ms"`
	HTTPStatus   int         `json:"http_status,omitempty"`
	ErrorCode    string      `json:"error_code,omitempty"`
	ErrorMessage string      `json:"error_message,omitempty"`
	Hint         string      `json:"hint,omitempty"`
	ModelOutput  string      `json:"model_output,omitempty"`
	Assessment   *Assessment `json:"assessment,omitempty"`
	Usage        Usage       `json:"usage"`
}

// A saved channel can be diagnosed without a policy, cache hit or fallback.
// Manual diagnostics share the normal concurrency/RPM reservations and health.
func (s *Server) testChannel(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Revision int64 `json:"expected_revision"`
	}
	if err := readJSON(w, r, &in); err != nil {
		return err
	}
	c, err := scanChannel(s.Store.DB.QueryRowContext(r.Context(), "SELECT "+channelColumns+" FROM audit_model_channels c JOIN provider_credentials k ON k.id=c.credential_id WHERE c.id=$1", r.PathValue("id")))
	if err != nil {
		return err
	}
	if in.Revision != c.Revision {
		return ErrConflict
	}
	cfg := c.Inference(DefaultSettings())
	cfg.Prompt, cfg.ResultCacheTTL = channelTestPrompt, 0
	started := time.Now()
	result := ChannelTestResult{ChannelID: c.ID, ChannelName: c.Name, Model: c.Model, APIFormat: cfg.apiFormat(), Endpoint: cfg.inferenceURL(), TimeoutMS: cfg.TimeoutMS, MaxTokens: cfg.MaxTokens}
	key := ""
	finish := func(err error) error {
		result.LatencyMS = time.Since(started).Milliseconds()
		result.OK = err == nil
		if err != nil {
			result.ErrorCode = errorCode(err)
			result.ErrorMessage = diagnosticText(storedAuditError(err), key)
			var upstream *upstreamFailure
			if errors.As(err, &upstream) {
				result.HTTPStatus = upstream.httpStatus
			}
			result.Hint = channelTestHint(result.ErrorCode, result.HTTPStatus)
		}
		return writeJSON(w, 200, result)
	}
	if err := cfg.Validate(); err != nil {
		return finish(problem(400, "invalid_config", err.Error()))
	}
	select {
	case s.trialSlots <- struct{}{}:
		defer func() { <-s.trialSlots }()
	default:
		return finish(problem(503, "trial_capacity_exceeded", "后台测试并发已满，请稍后重试"))
	}
	key, err = s.Store.CredentialForConfig(r.Context(), cfg)
	if err != nil {
		return finish(err)
	}
	candidate := routeCandidate{channel: c, cfg: cfg, binding: ChannelBinding{ChannelID: c.ID, Priority: 1, Weight: 1, Enabled: true}, signature: digest(c.CacheEpoch + key)}
	selected, release, err := s.Engine.acquire([]routeCandidate{candidate}, DefaultSettings(), nil, time.Now(), func(int) int { return 0 })
	if err != nil {
		return finish(err)
	}
	if selected == nil {
		return finish(problem(503, "channel_capacity_exceeded", "该通道并发已满或配置已更新，请稍后重试"))
	}
	// Honor credential revocation and edits that happened during reservation.
	var active bool
	err = s.Store.DB.QueryRowContext(r.Context(), "SELECT k.active FROM audit_model_channels c JOIN provider_credentials k ON k.id=c.credential_id WHERE c.id=$1 AND c.revision=$2", c.ID, c.Revision).Scan(&active)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrConflict
	} else if err == nil && !active {
		err = problem(503, "credential_unavailable", "连接密钥已停用")
	}
	if err != nil {
		release(err, false)
		return finish(err)
	}
	selected.attemptAt = time.Now()
	ctx := context.WithValue(r.Context(), upstreamStatusKey{}, &result.HTTPStatus)
	assessment, usage, output, err := s.Engine.Assess(ctx, cfg, key, channelTestInput)
	release(err, usage.Attempted)
	result.Attempted, result.Usage = usage.Attempted, usage
	result.Usage.ActualModel = diagnosticText(usage.ActualModel, key)
	result.Usage.UpstreamRequestID = diagnosticText(usage.UpstreamRequestID, key)
	result.ModelOutput = diagnosticText(output, key)
	if err == nil {
		assessment.Reason = diagnosticText(assessment.Reason, key)
		result.Assessment = &assessment
	}
	return finish(err)
}

func diagnosticText(value, key string) string {
	if key != "" {
		value = strings.ReplaceAll(value, key, "[隐去]")
	}
	return storedModelOutput(value)
}

func channelTestHint(code string, status int) string {
	if status == 404 || status == 405 {
		return "检查实际请求地址和接口类型；基础地址不要重复包含 /responses 或 /chat/completions。确认该路径及模型在供应商处可用。"
	}
	switch code {
	case "upstream_auth_failed":
		return "检查 API Key 是否正确、是否过期，以及该密钥是否有模型或项目访问权限。"
	case "upstream_rate_limited":
		return "检查供应商配额、余额及调用频率；稍后重试。"
	case "upstream_config_invalid":
		return "根据上游错误检查模型名称、接口类型和输出 token 上限，确认所选模型支持该接口。"
	case "invalid_model_response":
		return "上游已返回响应，但未得到完整审核结果；检查接口类型、模型是否支持 JSON 输出，或增加最大输出 tokens。"
	case "upstream_timeout":
		return "可增加该通道的单次超时（最多 30000 ms）；同时检查服务器到上游的网络连接。"
	case "upstream_unavailable":
		return "检查服务器到 API 地址的 DNS、网络、TLS 和代理配置，或稍后重试。"
	case "credential_unavailable":
		return "请先在连接密钥中启用或重新配置该连接。"
	}
	return "请根据错误详情调整配置后重新测试。"
}
