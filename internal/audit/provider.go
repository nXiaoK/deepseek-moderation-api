package audit

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const ProviderDeepSeek = "deepseek"
const ProviderGrok = "grok_via_sub2api"

func (c PolicyConfig) ProviderID() string {
	if c.Provider == "" {
		return ProviderDeepSeek
	}
	return c.Provider
}
func validateProviderURL(provider, base string) error {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || (u.Path != "" && u.Path != "/" && u.Path != "/v1" && u.Path != "/v1/") {
		return errors.New("连接地址必须为服务根地址或 /v1，不能包含凭据、查询参数或其他路径")
	}
	switch provider {
	case ProviderDeepSeek:
		if u.Scheme != "https" || u.Host != "api.deepseek.com" {
			return errors.New("DeepSeek 地址必须为 https://api.deepseek.com")
		}
	case ProviderGrok:
		if u.Scheme != "https" && u.Scheme != "http" {
			return errors.New("sub2api 连接只支持 HTTP(S)")
		}
		origin := u.Scheme + "://" + u.Host
		allowed := false
		for _, value := range strings.Split(os.Getenv("AUDIT_SUB2API_ORIGINS"), ",") {
			if strings.TrimSpace(value) == origin {
				allowed = true
			}
		}
		if !allowed {
			return errors.New("请先将 sub2api 源地址加入服务器 AUDIT_SUB2API_ORIGINS，再保存连接")
		}
	default:
		return errors.New("不支持的审核供应商")
	}
	return nil
}
func providerRoot(base string) string {
	base = strings.TrimRight(base, "/")
	return strings.TrimSuffix(base, "/v1")
}
func (c PolicyConfig) chatURL() string {
	if c.ProviderID() == ProviderGrok {
		return providerRoot(c.BaseURL) + "/v1/chat/completions"
	}
	return strings.TrimRight(c.BaseURL, "/") + "/chat/completions"
}
func (c PolicyConfig) providerLabel() string {
	if c.ProviderID() == ProviderGrok {
		return "Grok/sub2api"
	}
	return "DeepSeek"
}

// Model listing is an optional connection check. Inference and publication
// do not depend on it, since some compatible gateways expose only chat.
// validateProviderURL still enforces AUDIT_SUB2API_ORIGINS before credentials
// are sent to the configured origin.
type GrokModels struct {
	Models []string `json:"models"`
}

func (e *Engine) ProbeGrok(ctx context.Context, cfg PolicyConfig, key string) (*GrokModels, error) {
	if err := validateProviderURL(ProviderGrok, cfg.BaseURL); err != nil {
		return nil, problem(400, "invalid_connection", err.Error())
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, providerRoot(cfg.BaseURL)+"/v1/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	res, err := e.Client.Do(req)
	if err != nil {
		return nil, problem(503, "grok_connection_unavailable", "无法连接 Grok API，请检查 sub2api 地址")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, problem(502, "grok_models_unavailable", "无法读取 /v1/models，请检查地址和 API Key；若网关不提供模型列表，可手动填写模型后试跑")
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, 131073))
	if err != nil || len(raw) > 131072 {
		return nil, problem(502, "invalid_model_list", "模型列表响应无效")
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &body) != nil || body.Data == nil {
		return nil, problem(502, "invalid_model_list", "接口未返回标准模型列表，可手动填写模型后试跑")
	}
	result := &GrokModels{Models: []string{}}
	seen := map[string]bool{}
	for _, item := range body.Data {
		if item.ID != "" && len(item.ID) <= 100 && !seen[item.ID] {
			result.Models = append(result.Models, item.ID)
			seen[item.ID] = true
		}
	}
	return result, nil
}
func (s *Server) probeConnection(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Config PolicyConfig `json:"config"`
	}
	if err := readJSON(w, r, &in); err != nil {
		return err
	}
	if err := in.Config.Validate(); err != nil {
		return problem(400, "invalid_config", err.Error())
	}
	key, err := s.Store.CredentialForConfig(r.Context(), in.Config)
	if err != nil {
		return err
	}
	if in.Config.ProviderID() != ProviderGrok {
		return problem(400, "unsupported_probe", "DeepSeek 连接请通过审核试跑验证")
	}
	caps, err := s.Engine.ProbeGrok(r.Context(), in.Config, key)
	if err != nil {
		return err
	}
	return writeJSON(w, 200, caps)
}
func parseGrokUsage(raw []byte) Usage {
	var w struct {
		Prompt       *int `json:"prompt_tokens"`
		Completion   *int `json:"completion_tokens"`
		Total        *int `json:"total_tokens"`
		InputDetails struct {
			Cached *int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		OutputDetails struct {
			Reasoning int `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	}
	u := Usage{Attempted: true}
	if json.Unmarshal(raw, &w) != nil || w.Prompt == nil || w.Completion == nil || w.Total == nil {
		return u
	}
	p, o, total := *w.Prompt, *w.Completion, *w.Total
	r := w.OutputDetails.Reasoning
	if p < 0 || o < 0 || r < 0 || total < p || o > total-p {
		return u
	}
	gap := total - p - o
	if gap != 0 {
		if gap != r {
			return u
		}
		o += gap
	}
	if r > o {
		return u
	}
	u.PromptTokens = p
	u.CompletionTokens = o
	u.TotalTokens = total
	u.ReasoningTokens = r
	u.Reported = true
	if w.InputDetails.Cached != nil && *w.InputDetails.Cached >= 0 && *w.InputDetails.Cached <= p {
		miss := p - *w.InputDetails.Cached
		u.CacheHitTokens = w.InputDetails.Cached
		u.CacheMissTokens = &miss
	}
	return u
}
