package audit

import (
	"bufio"
	"bytes"
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
func (c PolicyConfig) inferenceURL() string {
	if c.ProviderID() == ProviderGrok {
		return providerRoot(c.BaseURL) + "/v1/responses"
	}
	return strings.TrimRight(c.BaseURL, "/") + "/chat/completions"
}
func (c PolicyConfig) providerLabel() string {
	if c.ProviderID() == ProviderGrok {
		return "Grok/sub2api"
	}
	return "DeepSeek"
}

const grokStreamLimit = 262144
const grokOutputLimit = 65536

type grokResponse struct {
	ID         string          `json:"id"`
	Model      string          `json:"model"`
	Status     string          `json:"status"`
	Usage      json.RawMessage `json:"usage"`
	OutputText string          `json:"output_text"`
	Output     []struct {
		Content []struct {
			Type    string `json:"type"`
			Text    string `json:"text"`
			Refusal any    `json:"refusal"`
		} `json:"content"`
	} `json:"output"`
	Error json.RawMessage `json:"error"`
}

func (e *Engine) assessGrok(ctx context.Context, cfg PolicyConfig, key, input string) (Assessment, Usage, string, error) {
	payload := map[string]any{
		"model":             cfg.Model,
		"stream":            true,
		"temperature":       0,
		"max_output_tokens": cfg.MaxTokens,
		"instructions":      cfg.Prompt,
		"input":             "<user_input>" + input + "</user_input>",
		"text":              map[string]any{"format": map[string]string{"type": "json_object"}},
		"reasoning":         map[string]string{"effort": "none"},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return Assessment{}, Usage{}, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.inferenceURL(), bytes.NewReader(raw))
	if err != nil {
		return Assessment{}, Usage{}, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+key)
	attempt := Usage{Attempted: true}
	res, err := e.Client.Do(req)
	if err != nil {
		return Assessment{}, attempt, "", upstreamCallError(err, cfg)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return Assessment{}, attempt, "", problem(503, "upstream_unavailable", cfg.providerLabel()+" 请求失败，请检查连接、密钥和额度")
	}
	content, env, err := readGrokResponse(res)
	usage := parseGrokUsage(env.Usage)
	if len(env.ID) <= 200 {
		usage.UpstreamRequestID = env.ID
	}
	if len(env.Model) <= 200 {
		usage.ActualModel = env.Model
	}
	if err != nil {
		var ae *APIError
		if errors.As(err, &ae) {
			return Assessment{}, usage, content, ae
		}
		return Assessment{}, usage, content, upstreamCallError(err, cfg)
	}
	if env.Status == "failed" || env.Status == "incomplete" || env.Status == "cancelled" || len(env.Error) > 0 && string(env.Error) != "null" {
		return Assessment{}, usage, content, problem(502, "invalid_model_response", "模型未完整返回审核结果")
	}
	assessment, err := ParseAssessment([]byte(content))
	if err != nil {
		return Assessment{}, usage, content, problem(502, "invalid_model_response", err.Error())
	}
	return assessment, usage, content, nil
}

func readGrokResponse(res *http.Response) (string, grokResponse, error) {
	ct := res.Header.Get("Content-Type")
	if strings.Contains(ct, "event-stream") {
		return parseGrokSSE(res.Body)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, grokStreamLimit+1))
	if err != nil {
		return "", grokResponse{}, err
	}
	if len(body) > grokStreamLimit {
		return "", grokResponse{}, problem(502, "invalid_model_response", "模型响应超过上限或读取失败")
	}
	trim := bytes.TrimSpace(body)
	if bytes.HasPrefix(trim, []byte("data:")) || bytes.HasPrefix(trim, []byte("event:")) {
		return parseGrokSSE(bytes.NewReader(body))
	}
	return parseGrokResponseJSON(body)
}

func parseGrokResponseJSON(body []byte) (string, grokResponse, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := uniqueValue(dec, 0); err != nil {
		return "", grokResponse{}, problem(502, "invalid_model_response", "模型响应 JSON 无效")
	}
	if _, err := dec.Token(); err != io.EOF {
		return "", grokResponse{}, problem(502, "invalid_model_response", "模型响应包含多余内容")
	}
	var env grokResponse
	if json.Unmarshal(body, &env) != nil {
		return "", grokResponse{}, problem(502, "invalid_model_response", "模型响应 JSON 无效")
	}
	text, refused := grokMessageText(env)
	if refused || env.Status != "completed" || text == "" {
		return text, env, problem(502, "invalid_model_response", "模型未完整返回审核结果")
	}
	return text, env, nil
}

func parseGrokSSE(r io.Reader) (string, grokResponse, error) {
	br := bufio.NewReaderSize(io.LimitReader(r, grokStreamLimit+1), 32*1024)
	var (
		eventType string
		data      []string
		text      strings.Builder
		env       grokResponse
		n         int
	)
	flush := func() error {
		if eventType == "" && len(data) == 0 {
			return nil
		}
		payload := strings.Join(data, "\n")
		eventType, data = "", nil
		if payload == "" || payload == "[DONE]" {
			return nil
		}
		var ev struct {
			Type     string          `json:"type"`
			Delta    json.RawMessage `json:"delta"`
			Text     string          `json:"text"`
			Response json.RawMessage `json:"response"`
			Usage    json.RawMessage `json:"usage"`
			Status   string          `json:"status"`
		}
		if json.Unmarshal([]byte(payload), &ev) != nil {
			return problem(502, "invalid_model_response", "模型响应 JSON 无效")
		}
		switch ev.Type {
		case "response.output_text.delta":
			text.WriteString(grokDeltaText(ev.Delta))
		case "response.output_text.done":
			if ev.Text != "" {
				text.Reset()
				text.WriteString(ev.Text)
			}
		case "response.failed", "error":
			return problem(502, "invalid_model_response", "模型未完整返回审核结果")
		case "response.completed", "response.incomplete":
			raw := ev.Response
			if len(raw) == 0 {
				raw = []byte(payload)
			}
			if json.Unmarshal(raw, &env) != nil {
				return problem(502, "invalid_model_response", "模型响应 JSON 无效")
			}
		}
		if ev.Type == "" && (len(ev.Response) > 0 || ev.Status != "" || len(ev.Usage) > 0) {
			raw := ev.Response
			if len(raw) == 0 {
				raw = []byte(payload)
			}
			_ = json.Unmarshal(raw, &env)
		}
		if text.Len() > grokOutputLimit {
			return problem(502, "invalid_model_response", "模型响应超过上限或读取失败")
		}
		return nil
	}
	for {
		line, err := br.ReadString('\n')
		n += len(line)
		if n > grokStreamLimit {
			return "", env, problem(502, "invalid_model_response", "模型响应超过上限或读取失败")
		}
		eof := err == io.EOF
		if err != nil && !eof {
			return "", env, err
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "":
			if err := flush(); err != nil {
				return "", env, err
			}
		case strings.HasPrefix(line, ":"):
		case strings.HasPrefix(line, "event:"):
			eventType = strings.TrimSpace(line[len("event:"):])
		case strings.HasPrefix(line, "data:"):
			value := line[len("data:"):]
			if strings.HasPrefix(value, " ") {
				value = value[1:]
			}
			data = append(data, value)
		}
		if eof {
			if err := flush(); err != nil {
				return "", env, err
			}
			break
		}
	}
	content := text.String()
	if full, refused := grokMessageText(env); refused {
		return content, env, problem(502, "invalid_model_response", "模型未完整返回审核结果")
	} else if full != "" {
		content = full
	}
	if content == "" {
		return content, env, problem(502, "invalid_model_response", "模型未完整返回审核结果")
	}
	return content, env, nil
}

func grokDeltaText(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var obj struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		return obj.Text
	}
	return ""
}

func grokMessageText(env grokResponse) (string, bool) {
	var text string
	for _, item := range env.Output {
		for _, part := range item.Content {
			if part.Type == "refusal" || part.Refusal != nil {
				return "", true
			}
			if part.Type == "output_text" || part.Type == "text" {
				text += part.Text
			}
		}
	}
	if env.OutputText != "" {
		return env.OutputText, false
	}
	return text, false
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
	u := Usage{Attempted: true}
	if len(raw) == 0 {
		return u
	}
	var chat struct {
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
	if json.Unmarshal(raw, &chat) == nil && chat.Prompt != nil && chat.Completion != nil && chat.Total != nil {
		return normalizeGrokUsage(*chat.Prompt, *chat.Completion, *chat.Total, chat.OutputDetails.Reasoning, chat.InputDetails.Cached)
	}
	var resp struct {
		Prompt       *int `json:"input_tokens"`
		Completion   *int `json:"output_tokens"`
		Total        *int `json:"total_tokens"`
		InputDetails struct {
			Cached *int `json:"cached_tokens"`
		} `json:"input_tokens_details"`
		OutputDetails struct {
			Reasoning int `json:"reasoning_tokens"`
		} `json:"output_tokens_details"`
	}
	if json.Unmarshal(raw, &resp) == nil && resp.Prompt != nil && resp.Completion != nil && resp.Total != nil {
		return normalizeGrokUsage(*resp.Prompt, *resp.Completion, *resp.Total, resp.OutputDetails.Reasoning, resp.InputDetails.Cached)
	}
	return u
}
func normalizeGrokUsage(p, o, total, r int, cached *int) Usage {
	u := Usage{Attempted: true}
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
	if cached != nil && *cached >= 0 && *cached <= p {
		miss := p - *cached
		u.CacheHitTokens = cached
		u.CacheMissTokens = &miss
	}
	return u
}
