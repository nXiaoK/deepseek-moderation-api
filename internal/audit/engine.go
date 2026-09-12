package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"time"
)

type Engine struct {
	Client *http.Client
	slots  chan struct{}
}

func NewEngine(concurrency int) *Engine {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = concurrency
	transport.ResponseHeaderTimeout = 30 * time.Second
	transport.DialContext = (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	return &Engine{Client: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirects disabled") }}, slots: make(chan struct{}, concurrency)}
}
func (e *Engine) Assess(ctx context.Context, cfg PolicyConfig, key, input string) (Assessment, Usage, error) {
	if err := cfg.Validate(); err != nil {
		return Assessment{}, Usage{}, problem(400, "invalid_config", err.Error())
	}
	select {
	case e.slots <- struct{}{}:
		defer func() { <-e.slots }()
	default:
		return Assessment{}, Usage{}, problem(503, "capacity_exceeded", "审核并发已满，请稍后重试")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(cfg.TimeoutMS)*time.Millisecond)
	defer cancel()
	payload := map[string]any{"model": cfg.Model, "stream": false, "thinking": map[string]string{"type": "disabled"}, "temperature": 0, "max_tokens": cfg.MaxTokens, "response_format": map[string]string{"type": "json_object"}, "messages": []map[string]string{{"role": "system", "content": cfg.Prompt}, {"role": "user", "content": "<user_input>" + input + "</user_input>"}}}
	if cfg.ProviderID() == ProviderGrok {
		delete(payload, "thinking")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return Assessment{}, Usage{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.chatURL(), bytes.NewReader(raw))
	if err != nil {
		return Assessment{}, Usage{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	attempt := Usage{Attempted: true}
	res, err := e.Client.Do(req)
	if err != nil {
		var ne net.Error
		if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &ne) && ne.Timeout() {
			return Assessment{}, attempt, problem(504, "upstream_timeout", cfg.providerLabel()+" 审核超时")
		}
		return Assessment{}, attempt, problem(503, "upstream_unavailable", cfg.providerLabel()+" 暂时不可用")
	}
	defer res.Body.Close()

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return Assessment{}, attempt, problem(503, "upstream_unavailable", cfg.providerLabel()+" 请求失败，请检查连接、密钥和额度")
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 65537))
	if err != nil || len(body) > 65536 {
		return Assessment{}, attempt, problem(502, "invalid_model_response", "模型响应超过上限或读取失败")
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
		return Assessment{}, attempt, problem(502, "invalid_model_response", "模型响应 JSON 无效")
	}
	if _, err = dec.Token(); err != io.EOF {
		return Assessment{}, attempt, problem(502, "invalid_model_response", "模型响应包含多余内容")
	}
	decodeErr := json.Unmarshal(body, &out)
	usage := Usage{Attempted: true}
	if cfg.ProviderID() == ProviderGrok {
		usage = parseGrokUsage(out.Usage)
	} else {
		_ = json.Unmarshal(out.Usage, &usage)
		usage.Attempted = true
	}
	if len(out.ID) <= 200 {
		usage.UpstreamRequestID = out.ID
	}
	if len(out.Model) <= 200 {
		usage.ActualModel = out.Model
	}
	if decodeErr != nil || len(out.Choices) != 1 || out.Choices[0].Finish != "stop" || out.Choices[0].Message.Refusal != nil {
		return Assessment{}, usage, problem(502, "invalid_model_response", "模型未完整返回审核结果")
	}
	assessment, err := ParseAssessment([]byte(out.Choices[0].Message.Content))
	if err != nil {
		return Assessment{}, usage, problem(502, "invalid_model_response", err.Error())
	}
	return assessment, usage, nil
}

func (s *Server) runAudit(ctx context.Context, p Policy, cfg PolicyConfig, version int, client, kind, text string) (Response, error) {
	id := randomToken("audit_")
	start := time.Now()
	key, err := s.Store.CredentialForConfig(ctx, cfg)

	var result Assessment
	var usage Usage
	var entry *CostReservation
	var cost *CostView
	cacheHit := false
	cacheKey := ""
	if err == nil && kind == "production" && cfg.ResultCacheTTL > 0 {
		cacheKey = s.Store.assessmentCacheKey(client, p, cfg, version, key, text)
		cached, cacheErr := s.Store.CachedAssessment(ctx, cacheKey)
		if cacheErr != nil {
			return Response{}, problem(503, "cache_unavailable", "审核缓存暂时不可用")
		}
		if cached != nil {
			result = *cached
			cacheHit = true
			usage.Reported = true
		}
	}
	if err == nil {
		entry, err = s.Store.ReserveCost(ctx, id, client, kind, p, cfg, version, text, cacheHit, start)
		if err != nil {
			return Response{}, err
		}
		if !cacheHit {
			result, usage, err = s.Engine.Assess(ctx, cfg, key, text)
		}
		settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		var settleErr error
		cost, settleErr = s.Store.SettleCost(settleCtx, entry, usage, time.Now())
		cancel()
		if settleErr != nil {
			return Response{}, problem(503, "cost_record_unavailable", "成本结算暂时不可用，预留费用待核对")
		}
		if err == nil && !cacheHit && cacheKey != "" {
			cacheCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
			_ = s.Store.CacheAssessment(cacheCtx, cacheKey, result, cfg.ResultCacheTTL)
			cancel()
		}
	}
	elapsed := time.Since(start).Milliseconds()
	l := AuditLog{UpstreamRequestID: usage.UpstreamRequestID, Provider: cfg.ProviderID(), ID: id, Kind: kind, PolicyID: p.ID, PolicyVersion: version, ClientID: client, Model: cfg.Model, Threshold: cfg.Threshold, LatencyMS: elapsed, Usage: usage, InputStored: cfg.StoreInput, CreatedAt: time.Now().UTC(), Cost: cost, CacheHit: cacheHit}
	if err == nil {
		l.Confidence = &result.Confidence
		l.Flagged = result.Confidence >= cfg.Threshold
		l.Reason = redact(result.Reason)
	} else {
		l.ErrorCode = "internal_error"
		var ae *APIError
		if errors.As(err, &ae) {
			l.ErrorCode = ae.Code
		}
	}
	logCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	if recordErr := s.Store.Record(logCtx, l, text, cfg.RetentionDays); recordErr != nil {
		return Response{}, problem(503, "record_unavailable", "审核记录暂时无法保存")
	}
	if err != nil {
		return Response{}, err
	}
	item := Result{Flagged: l.Flagged, Categories: map[string]bool{"custom_policy": l.Flagged}, Scores: map[string]float64{"custom_policy": result.Confidence}, Audit: AuditMetadata{1, p.ID, version, result.Confidence, cfg.Threshold, l.Reason}}
	return Response{Provider: cfg.ProviderID(), ID: id, Model: p.Alias, Results: []Result{item}, Usage: usage, LatencyMS: elapsed, Cost: cost, CacheHit: cacheHit}, nil
}
