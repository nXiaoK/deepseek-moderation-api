package audit

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"
)

const ProviderDeepSeek = "deepseek"
const ProviderGrok = "grok_via_sub2api"
const grokProtocol = "grok-v1"
const grokProtocolHeader = "X-Sub2api-Audit-Protocol"

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
		return providerRoot(c.BaseURL) + "/v1/audit/grok/chat/completions"
	}
	return strings.TrimRight(c.BaseURL, "/") + "/chat/completions"
}
func (c PolicyConfig) providerLabel() string {
	if c.ProviderID() == ProviderGrok {
		return "Grok/sub2api"
	}
	return "DeepSeek"
}

type GrokCapabilities struct {
	Protocol           string   `json:"protocol"`
	RecursionProtected bool     `json:"audit_recursion_protected"`
	Models             []string `json:"models"`
	MaxInput           int      `json:"max_input_characters"`
	MaxOutput          int      `json:"max_output_tokens"`
	ManagementPath     string   `json:"account_management_path"`
	BillingMode        string   `json:"billing_mode"`
}

func (e *Engine) ProbeGrok(ctx context.Context, cfg PolicyConfig, key string) (*GrokCapabilities, error) {
	if err := validateProviderURL(ProviderGrok, cfg.BaseURL); err != nil {
		return nil, problem(400, "invalid_connection", err.Error())
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, providerRoot(cfg.BaseURL)+"/v1/audit/grok/capabilities", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	res, err := e.Client.Do(req)
	if err != nil {
		return nil, problem(503, "grok_connection_unavailable", "无法连接 sub2api 审核入口")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 || res.Header.Get(grokProtocolHeader) != grokProtocol {
		return nil, problem(503, "grok_connection_unverified", "sub2api 尚未启用受限 Grok 审核入口，或服务密钥/分组权限不匹配")
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, 65537))
	if err != nil || len(raw) > 65536 {
		return nil, problem(502, "invalid_capabilities", "连接能力响应无效")
	}
	var caps GrokCapabilities
	if json.Unmarshal(raw, &caps) != nil || caps.Protocol != grokProtocol || !caps.RecursionProtected || caps.MaxOutput < cfg.MaxTokens || caps.MaxInput < 64000 || len(caps.Models) == 0 {
		return nil, problem(502, "invalid_capabilities", "sub2api 审核能力不满足要求")
	}
	caps.ManagementPath = "/admin/accounts" // never navigate a URL supplied by the peer
	return &caps, nil
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
func (s *Server) verifyGrokPublication(ctx context.Context, id string, revision int64, rollback int) error {
	p, err := s.Store.Policy(ctx, id)
	if err != nil {
		return err
	}
	if p.DraftRevision != revision {
		return ErrConflict
	}
	cfg := p.Draft
	if rollback > 0 {
		var raw []byte
		if err = s.Store.DB.QueryRowContext(ctx, "SELECT config FROM audit_policy_versions WHERE policy_id=$1 AND version=$2", id, rollback).Scan(&raw); err != nil {
			return err
		}
		if err = json.Unmarshal(raw, &cfg); err != nil {
			return err
		}
	}
	if cfg.ProviderID() != ProviderGrok {
		return nil
	}
	key, err := s.Store.CredentialForConfig(ctx, cfg)
	if err != nil {
		return err
	}
	caps, err := s.Engine.ProbeGrok(ctx, cfg, key)
	if err != nil {
		return err
	}
	if !slices.Contains(caps.Models, cfg.Model) {
		return problem(400, "model_not_allowed", "所选模型不在 sub2api 审核分组的明确允许列表中")
	}
	return nil
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
