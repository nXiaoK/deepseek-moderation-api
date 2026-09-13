package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type ChannelHealth struct {
	Status        string    `json:"status"`
	InFlight      int       `json:"in_flight"`
	Calls         int64     `json:"calls"`
	Failures      int64     `json:"failures"`
	CooldownUntil time.Time `json:"cooldown_until,omitempty"`
}
type channelState struct {
	revision         int64
	signature        string
	inFlight         int
	consecutive      int
	calls, failures  int64
	until            time.Time
	permanent, probe bool
}
type routeCandidate struct {
	channel        ModelChannel
	binding        ChannelBinding
	cfg            PolicyConfig
	key, signature string
}
type upstreamFailure struct {
	*APIError
	cooldown  time.Duration
	permanent bool
}

func (e *upstreamFailure) Unwrap() error { return e.APIError }
func classifyUpstream(res *http.Response) error {
	e := &upstreamFailure{APIError: &APIError{503, "upstream_unavailable", "模型请求失败"}}
	switch {
	case res.StatusCode == 429:
		e.Code = "upstream_rate_limited"
		e.Message = "模型请求限流"
		e.cooldown = 30 * time.Second
		if n, err := strconv.ParseInt(res.Header.Get("Retry-After"), 10, 64); err == nil && n >= 0 && n <= 86400 {
			e.cooldown = time.Duration(n) * time.Second
		} else if at, err := http.ParseTime(res.Header.Get("Retry-After")); err == nil && at.After(time.Now()) {
			e.cooldown = time.Until(at)
			if e.cooldown > 24*time.Hour {
				e.cooldown = 24 * time.Hour
			}
		}
	case res.StatusCode == 401 || res.StatusCode == 403:
		e.Code = "upstream_auth_failed"
		e.Message = "模型密钥或权限无效，请修正连接后重新保存通道"
		e.permanent = true
	case res.StatusCode >= 400 && res.StatusCode < 500:
		e.Code = "upstream_config_invalid"
		e.Message = "模型名称或参数不受支持，请修正后重新保存通道"
		e.permanent = true
	}
	if detail := upstreamErrorDetail(res); detail != "" {
		e.Message += fmt.Sprintf("（HTTP %d：%s）", res.StatusCode, detail)
	}
	return e
}

// Only retain the structured error message, never a raw response or HTML page.
// Upstreams may echo credentials in errors; redact before returning or storing.
func upstreamErrorDetail(res *http.Response) string {
	if res.Body == nil {
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 8193))
	if err != nil || len(body) > 8192 {
		return ""
	}
	var payload struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Detail string `json:"detail"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return ""
	}
	message := payload.Error.Message
	if message == "" {
		message = payload.Detail
	}
	message = strings.Join(strings.Fields(storedModelOutput(message)), " ")
	if utf8.RuneCountInString(message) > 512 {
		message = string([]rune(message)[:512]) + "…"
	}
	return message
}
func (e *Engine) resetChannel(id string) {
	e.routeMu.Lock()
	defer e.routeMu.Unlock()
	if st := e.routes[id]; st != nil {
		st.signature = ""
		st.until = time.Time{}
		st.permanent = false
		st.probe = false
		st.consecutive = 0
	}
}
func (e *Engine) channelHealth(id string) ChannelHealth {
	e.routeMu.Lock()
	defer e.routeMu.Unlock()
	h := ChannelHealth{Status: "ready"}
	if st := e.routes[id]; st != nil {
		h.InFlight = st.inFlight
		h.Calls = st.calls
		h.Failures = st.failures
		h.CooldownUntil = st.until
		if st.permanent {
			h.Status = "configuration_error"
		} else if st.until.After(time.Now()) {
			h.Status = "cooling"
		} else if !st.until.IsZero() {
			h.Status = "half_open"
		}
	}
	return h
}

// Selection and capacity reservation are atomic. RNG injection makes boundary
// tests deterministic without probabilistic/flaky distribution assertions.
func (e *Engine) acquire(candidates []routeCandidate, used map[string]bool, now time.Time, draw func(int) int) (*routeCandidate, func(error, bool), error) {
	e.routeMu.Lock()
	defer e.routeMu.Unlock()
	if len(e.slots) >= cap(e.slots) {
		return nil, nil, problem(503, "capacity_exceeded", "审核并发已满，请稍后重试")
	}
	priority := 101
	weight := 0
	eligible := []int{}
	for i, c := range candidates {
		if used[c.channel.ID] {
			continue
		}
		st := e.routes[c.channel.ID]
		if st == nil {
			st = &channelState{}
			e.routes[c.channel.ID] = st
		}
		if c.channel.Revision < st.revision {
			continue
		}
		if st.signature != c.signature {
			st.revision = c.channel.Revision
			st.signature = c.signature
			st.until = time.Time{}
			st.permanent = false
			st.probe = false
			st.consecutive = 0
		}
		if st.permanent || st.until.After(now) || st.probe || st.inFlight >= c.channel.MaxConcurrency {
			continue
		}
		if c.binding.Priority < priority {
			priority = c.binding.Priority
			eligible = nil
			weight = 0
		}
		if c.binding.Priority == priority {
			eligible = append(eligible, i)
			weight += c.binding.Weight
		}
	}
	if len(eligible) == 0 {
		return nil, nil, nil
	}
	pick := draw(weight)
	chosen := eligible[0]
	for _, i := range eligible {
		pick -= candidates[i].binding.Weight
		if pick < 0 {
			chosen = i
			break
		}
	}
	c := candidates[chosen]
	st := e.routes[c.channel.ID]
	st.inFlight++
	if !st.until.IsZero() {
		st.probe = true
	}
	release := func(err error, sent bool) {
		e.routeMu.Lock()
		defer e.routeMu.Unlock()
		st.inFlight--
		if sent {
			st.calls++
			if err != nil {
				st.failures++
			}
		}
		// A completed old request must not poison a newly edited connection.
		if st.signature != c.signature {
			return
		}
		wasProbe := st.probe
		st.probe = false
		if err == nil {
			st.consecutive = 0
			st.until = time.Time{}
			return
		}
		if !sent {
			return
		}
		st.consecutive++
		var upstream *upstreamFailure
		if errors.As(err, &upstream) {
			if upstream.permanent {
				st.permanent = true
			}
			if upstream.cooldown > 0 {
				st.until = time.Now().Add(upstream.cooldown)
				return
			}
		}
		if wasProbe || st.consecutive >= 3 {
			st.until = time.Now().Add(30 * time.Second)
		}
	}
	return &c, release, nil
}
func retryable(err error) bool {
	var ae *APIError
	if !errors.As(err, &ae) {
		return false
	}
	switch ae.Code {
	case "upstream_timeout", "upstream_unavailable", "upstream_rate_limited", "upstream_auth_failed", "upstream_config_invalid", "invalid_model_response", "credential_unavailable":
		return true
	}
	return false
}
func (s *Store) budgetEnabled(ctx context.Context, client string) (bool, error) {
	var enabled bool
	err := s.DB.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM client_budgets WHERE client_id=$1 AND (daily_limit IS NOT NULL OR monthly_limit IS NOT NULL))", client).Scan(&enabled)
	return enabled, err
}
func (s *Store) requestCost(ctx context.Context, id string) (*CostView, error) {
	var known, held int64
	var total, pending, estimated int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*),COUNT(*) FILTER(WHERE amount_pico IS NULL),COUNT(*) FILTER(WHERE status='estimated'),COALESCE(SUM(amount_pico),0),COALESCE(SUM(reserved_pico) FILTER(WHERE amount_pico IS NULL),0) FROM audit_costs WHERE request_id=$1`, id).Scan(&total, &pending, &estimated, &known, &held)
	if err != nil {
		return nil, err
	}
	if total == 0 {
		return nil, nil
	}
	amount := picoString(known)
	v := &CostView{Status: "calculated", AmountCNY: &amount, ReservedCNY: picoString(held), Period: "multiple_attempts", Note: "所有调用尝试的费用合计"}
	if estimated > 0 {
		v.Status = "estimated"
	}
	if pending > 0 {
		v.Status = "pending"
		v.AmountCNY = nil
		v.Note = "存在待核对调用；已知费用小计 ¥" + amount
	}
	return v, nil
}
func (s *Server) runAudit(ctx context.Context, p Policy, channels []ModelChannel, client, kind, input, onlyChannel string, images ...AuditImage) (Response, error) {
	id := randomToken("audit_")
	start := time.Now()
	a, _ := ctx.Value(requestAuditKey{}).(*requestAudit)
	if a != nil {
		id, start = a.log.ID, a.log.CreatedAt
	}
	response := Response{ID: id, Model: p.Alias, Usage: Usage{Reported: true}}
	log := AuditLog{ID: id, Kind: kind, PolicyID: p.ID, ClientID: client, Threshold: p.Config.Threshold, InputStored: p.Config.StoreInput, CreatedAt: start.UTC(), Attempts: []AuditAttempt{}}
	if a != nil {
		log.Request = a.log.Request
	}
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(p.Config.TotalTimeoutMS)*time.Millisecond)
	defer cancel()
	result, err := s.executeRoute(callCtx, p, channels, client, kind, input, onlyChannel, &response, &log, images...)
	if err != nil && callCtx.Err() != nil {
		err = problem(504, "audit_timeout", "审核已取消或总调用时限耗尽")
	}
	// One bounded detached context covers the complete accounting/logging tail.
	finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), 4*time.Second)
	defer finishCancel()
	cost, costErr := s.Store.requestCost(finishCtx, id)
	if costErr != nil {
		err = problem(503, "cost_record_unavailable", "费用汇总暂时不可用")
	}
	response.Cost = cost
	response.LatencyMS = time.Since(start).Milliseconds()
	response.AttemptCount = log.AttemptCount
	log.Cost = cost
	log.LatencyMS = response.LatencyMS
	log.Usage = response.Usage
	log.ChannelID = response.ChannelID
	log.Provider = response.Provider
	log.Model = response.ActualModel
	log.CacheHit = response.CacheHit
	var verdict Result
	if err == nil {
		verdict = moderationResult(p, result)
		log.Confidence = &result.Confidence
		log.Flagged = verdict.Flagged
		log.Reason = verdict.Audit.Reason
	} else {
		log.ErrorCode = errorCode(err)
		log.ErrorMessage = storedAuditError(err)
	}
	if a != nil {
		log.Request.InputScope = response.InputScope
		log.Request.TextOnlyFallback = response.InputScope == "text_only"
		log.Request.HTTPStatus = auditHTTPStatus(err)
		if err == nil {
			log.Request.Stage = "completed"
		}
		a.log, a.input, a.days = log, input, p.Config.RetentionDays
	}
	if recordErr := s.Store.Record(finishCtx, log, input, p.Config.RetentionDays); recordErr != nil {
		if a != nil {
			a.log.Request.Stage = "recording"
		}
		return response, problem(503, "record_unavailable", "审核记录暂时无法保存")
	}
	if a != nil {
		a.recorded = true
	}
	if err != nil {
		return response, err
	}
	response.Results = []Result{verdict}
	if kind == "test" {
		response.Attempts = log.Attempts
	}
	return response, nil
}

// Only expose our typed, user-facing errors; arbitrary errors may contain DSNs
// or credentials. Bound and redact the message before persisting it.
func storedAuditError(err error) string {
	var ae *APIError
	if errors.As(err, &ae) {
		return storedModelOutput(ae.Message)
	}
	return "服务内部错误，请检查服务运行日志"
}
func errorCode(err error) string {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Code
	}
	return "internal_error"
}
func (s *Server) executeRoute(ctx context.Context, p Policy, channels []ModelChannel, client, kind, input, onlyChannel string, response *Response, log *AuditLog, images ...AuditImage) (Assessment, error) {
	if err := p.Config.Validate(); err != nil {
		return Assessment{}, problem(400, "invalid_config", err.Error())
	}
	budget := false
	var err error
	if kind == "production" {
		budget, err = s.Store.budgetEnabled(ctx, client)
		if err != nil {
			return Assessment{}, err
		}
	}
	bindings := map[string]ChannelBinding{}
	for _, b := range p.Config.Channels {
		bindings[b.ChannelID] = b
	}
	if onlyChannel != "" {
		b, ok := bindings[onlyChannel]
		if !ok || !b.Enabled {
			return Assessment{}, problem(400, "invalid_channel", "请选择策略中已启用的通道")
		}
	}
	raw, _ := json.Marshal(p.Config)
	candidates := []routeCandidate{}
	excludedPrice := false
	excludedEmpty := false
	for _, c := range channels {
		b, ok := bindings[c.ID]
		if !ok || !b.Enabled || !c.Enabled || !c.CredentialActive || onlyChannel != "" && onlyChannel != c.ID {
			continue
		}
		cfg := c.Inference(p.Config)
		if c.TextOnly && len(images) > 0 && strings.TrimSpace(input) == "" {
			excludedEmpty = true
			continue
		}
		if !c.TextOnly {
			cfg.ImageCount = len(images)
		}
		cfg.ConnectionRevision = digest(string(raw) + c.ID + c.CacheEpoch)
		if err = cfg.Validate(); err != nil {
			return Assessment{}, problem(400, "invalid_channel", err.Error())
		}
		if budget {
			card, e := s.Store.Price(ctx, c.Model, time.Now())
			if e != nil {
				return Assessment{}, e
			}
			if card == nil || cfg.ImageCount > 0 {
				excludedPrice = true
				continue
			}
		}
		key, e := s.Store.CredentialForConfig(ctx, cfg)
		if e != nil {
			if errorCode(e) == "credential_unavailable" {
				continue
			}
			return Assessment{}, e
		}
		candidates = append(candidates, routeCandidate{c, b, cfg, key, digest(c.CacheEpoch + key)})
	}
	used := map[string]bool{}
	lastErr := problem(503, "no_available_channel", "当前没有可用的审核模型通道")
	if len(candidates) == 0 && excludedPrice {
		lastErr = problem(503, "pricing_unavailable", "预算设置下没有价格可估算的模型通道（图片用量暂不支持预估）")
	} else if len(candidates) == 0 && excludedEmpty {
		lastErr = problem(400, "empty_input", "文本模型跳过图片后没有可审核文本")
	}
	limit := p.Config.MaxAttempts
	if onlyChannel != "" {
		limit = 1
	}
	for log.AttemptCount < limit {
		if ctx.Err() != nil {
			return Assessment{}, problem(504, "audit_timeout", "审核已取消或总调用时限耗尽")
		}
		c, release, e := s.Engine.acquire(candidates, used, time.Now(), rand.IntN)
		if e != nil {
			return Assessment{}, e
		}
		if c == nil {
			break
		}
		used[c.channel.ID] = true
		// Recheck administrative stops before every attempt, including a fallback.
		var active bool
		e = s.Store.DB.QueryRowContext(ctx, `SELECT c.enabled AND k.active AND ($3 OR p.enabled) FROM audit_model_channels c JOIN provider_credentials k ON k.id=c.credential_id JOIN audit_policies p ON p.id=$2 WHERE c.id=$1 AND c.revision=$4`, c.channel.ID, p.ID, kind == "test", c.channel.Revision).Scan(&active)
		if errors.Is(e, sql.ErrNoRows) || e == nil && !active {
			release(nil, false)
			continue
		}
		if e != nil {
			release(e, false)
			return Assessment{}, e
		}
		key, e := s.Store.CredentialForConfig(ctx, c.cfg)
		if e != nil || key != c.key {
			release(e, false)
			if e != nil && errorCode(e) != "credential_unavailable" {
				return Assessment{}, e
			}
			continue
		}
		attemptImages := images
		if c.channel.TextOnly {
			attemptImages = nil
		}
		scope := auditInputScope(c.channel.TextOnly, images)
		attempt := AuditAttempt{InputScope: scope, ImageCount: len(attemptImages), ID: randomToken("cost_"), ChannelID: c.channel.ID, ChannelName: c.channel.Name, Provider: c.channel.Provider, Model: c.channel.Model}
		at := time.Now()
		assessment := Assessment{}
		cacheKey := ""
		cached := false
		cachedModel := ""
		if kind == "production" && c.cfg.ResultCacheTTL > 0 && imageInputCacheable(attemptImages) {
			cacheKey = s.Store.assessmentCacheKey(client, p, c.cfg, int(p.Revision), key, input, attemptImages...)
			value, cacheErr := s.Store.CachedAssessment(ctx, cacheKey)
			if cacheErr != nil {
				release(cacheErr, false)
				return Assessment{}, problem(503, "cache_unavailable", "审核缓存暂时不可用")
			}
			if value != nil {
				assessment = value.Assessment
				cachedModel = value.ActualModel
				cached = true
			}
		}
		entry, e := s.Store.ReserveCost(ctx, attempt.ID, client, kind, p, c.cfg, input, cached, at, response.ID, c.channel.ID)
		if e != nil {
			release(e, false)
			return Assessment{}, e
		}
		usage := Usage{Reported: true, ActualModel: cachedModel}
		output := ""
		if !cached {
			assessment, usage, output, e = s.Engine.Assess(ctx, c.cfg, key, input, attemptImages...)
		}
		if ctx.Err() != nil {
			release(ctx.Err(), false)
		} else {
			release(e, usage.Attempted)
		}
		response.ChannelID = c.channel.ID
		response.InputScope = scope
		response.Provider = c.channel.Provider
		response.ActualModel = c.channel.Model
		if usage.ActualModel != "" {
			response.ActualModel = usage.ActualModel
		}
		attempt.ModelOutput = storedModelOutput(output)
		log.ModelOutput = attempt.ModelOutput
		response.Usage.ActualModel = usage.ActualModel
		response.Usage.UpstreamRequestID = usage.UpstreamRequestID
		attempt.Sent = usage.Attempted
		attempt.CacheHit = cached
		attempt.Usage = usage
		attempt.LatencyMS = time.Since(at).Milliseconds()
		if e != nil {
			attempt.ErrorCode = errorCode(e)
			attempt.ErrorMessage = storedAuditError(e)
		}
		if usage.Attempted {
			log.AttemptCount++
		}
		settleCtx, settleCancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		attempt.Cost, err = s.Store.SettleCost(settleCtx, entry, usage, time.Now())
		settleCancel()
		log.Attempts = append(log.Attempts, attempt)
		response.Usage.PromptTokens += usage.PromptTokens
		response.Usage.CompletionTokens += usage.CompletionTokens
		response.Usage.TotalTokens += usage.TotalTokens
		response.Usage.ReasoningTokens += usage.ReasoningTokens
		response.Usage.Reported = response.Usage.Reported && usage.Reported
		if err != nil {
			return Assessment{}, problem(503, "cost_record_unavailable", "成本结算暂时不可用，预留费用待核对")
		}
		if e != nil {
			lastErr = e
			if !retryable(e) {
				return Assessment{}, e
			}
			continue
		}
		response.ChannelID = c.channel.ID
		response.Provider = c.channel.Provider
		response.ActualModel = c.channel.Model
		if usage.ActualModel != "" {
			response.ActualModel = usage.ActualModel
		}
		response.CacheHit = cached
		response.Usage.ActualModel = response.ActualModel
		response.Usage.UpstreamRequestID = usage.UpstreamRequestID
		if !cached {
			log.ModelOutput = storedModelOutput(output)
		}
		if !cached && cacheKey != "" {
			_ = s.Store.CacheAssessment(ctx, cacheKey, assessment, c.cfg.ResultCacheTTL, response.ActualModel)
		}
		return assessment, nil
	}
	return Assessment{}, lastErr
}
