package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type Engine struct {
	Client    *http.Client
	slots     chan struct{}
	routeMu   sync.Mutex
	routes    map[string]*channelState
	cacheGate keyedGate
}

func NewEngine(concurrency int) *Engine {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = concurrency
	transport.ResponseHeaderTimeout = 30 * time.Second
	transport.DialContext = (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	return &Engine{Client: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirects disabled") }}, slots: make(chan struct{}, concurrency), routes: map[string]*channelState{}}
}
func upstreamCallError(err error, cfg PolicyConfig) error {
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &ne) && ne.Timeout() {
		return problem(504, "upstream_timeout", cfg.providerLabel()+" 审核超时")
	}
	return problem(503, "upstream_unavailable", cfg.providerLabel()+" 暂时不可用")
}
func (e *Engine) Assess(ctx context.Context, cfg PolicyConfig, key, input string, images ...AuditImage) (Assessment, Usage, string, error) {
	if cfg.TextOnly {
		images = nil
	}
	if strings.TrimSpace(input) == "" && len(images) == 0 {
		return Assessment{}, Usage{}, "", problem(400, "empty_input", "没有可审核文本或图片")
	}
	if ctx.Err() != nil {
		return Assessment{}, Usage{}, "", upstreamCallError(ctx.Err(), cfg)
	}
	if err := cfg.Validate(); err != nil {
		return Assessment{}, Usage{}, "", problem(400, "invalid_config", err.Error())
	}
	select {
	case e.slots <- struct{}{}:
		defer func() { <-e.slots }()
	default:
		return Assessment{}, Usage{}, "", problem(503, "capacity_exceeded", "审核并发已满，请稍后重试")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(cfg.TimeoutMS)*time.Millisecond)
	defer cancel()
	if cfg.ProviderID() == ProviderGrok {
		return e.assessGrok(ctx, cfg, key, input, images...)
	}
	payload := map[string]any{"model": cfg.Model, "stream": false, "thinking": map[string]string{"type": "disabled"}, "max_tokens": cfg.MaxTokens, "response_format": map[string]string{"type": "json_object"}, "messages": []map[string]string{{"role": "system", "content": cfg.Prompt}, {"role": "user", "content": "<user_input>" + input + "</user_input>"}}}
	// Sampling controls are optional and some third-party model backends reject
	// them even at zero. Keep the existing setting only for official DeepSeek.
	if cfg.officialPricing() {
		payload["temperature"] = 0
	}
	if len(images) > 0 {
		content := []map[string]any{{"type": "text", "text": "<user_input>" + input + "</user_input>"}}
		for _, image := range images {
			content = append(content, map[string]any{"type": "image_url", "image_url": image})
		}
		payload["messages"] = []map[string]any{{"role": "system", "content": cfg.Prompt}, {"role": "user", "content": content}}
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
	req.Header.Set("Authorization", "Bearer "+key)
	attempt := Usage{Attempted: true}
	res, err := e.Client.Do(req)
	if err != nil {
		return Assessment{}, attempt, "", upstreamCallError(err, cfg)
	}
	defer res.Body.Close()

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return Assessment{}, attempt, "", classifyUpstream(res, key)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 65537))
	if err != nil {
		return Assessment{}, attempt, "", upstreamCallError(err, cfg)
	}
	if len(body) > 65536 {
		return Assessment{}, attempt, "", problem(502, "invalid_model_response", "模型响应超过上限或读取失败")
	}
	var out struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			Finish  string `json:"finish_reason"`
			Message struct {
				Content string `json:"content"`
				Refusal any    `json:"refusal"`
			} `json:"message"`
		} `json:"choices"`
		Usage json.RawMessage `json:"usage"`
	}
	// The provider envelope has extensible fields; the model's content is strict.
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err = uniqueValue(dec, 0); err != nil {
		return Assessment{}, attempt, "", problem(502, "invalid_model_response", "模型响应 JSON 无效")
	}
	if _, err = dec.Token(); err != io.EOF {
		return Assessment{}, attempt, "", problem(502, "invalid_model_response", "模型响应包含多余内容")
	}
	decodeErr := json.Unmarshal(body, &out)
	usage := Usage{Attempted: true}
	_ = json.Unmarshal(out.Usage, &usage)
	usage.Attempted = true
	if len(out.ID) <= 200 {
		usage.UpstreamRequestID = out.ID
	}
	if len(out.Model) <= 200 {
		usage.ActualModel = out.Model
	}
	content := ""
	if decodeErr == nil && len(out.Choices) == 1 {
		content = out.Choices[0].Message.Content
	}
	if decodeErr != nil || len(out.Choices) != 1 || out.Choices[0].Finish != "stop" || out.Choices[0].Message.Refusal != nil {
		return Assessment{}, usage, content, problem(502, "invalid_model_response", "模型未完整返回审核结果")
	}
	assessment, err := ParseAssessment([]byte(content))
	if err != nil {
		return Assessment{}, usage, content, problem(502, "invalid_model_response", err.Error())
	}
	return assessment, usage, content, nil
}
