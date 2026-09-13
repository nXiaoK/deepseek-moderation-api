package audit

import (
	"math/big"
	"net/http"
	"sort"
	"time"
)

type OperationalAlert struct {
	ID       string `json:"id"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Page     string `json:"page"`
}
type OperationsReport struct {
	UptimeSeconds      int64         `json:"uptime_seconds"`
	Runtime            RuntimeConfig `json:"runtime"`
	ActiveRequests     int           `json:"active_requests"`
	RequestBodyBytes   int64         `json:"request_body_bytes"`
	ModelInFlight      int           `json:"model_in_flight"`
	TrialInFlight      int           `json:"trial_in_flight"`
	MigrationVersion   int           `json:"migration_version"`
	PendingCosts       int64         `json:"pending_costs"`
	EstimatedCosts     int64         `json:"estimated_costs"`
	RunningEvaluations int64         `json:"running_evaluations"`
	Database           struct {
		Open      int   `json:"open"`
		InUse     int   `json:"in_use"`
		Idle      int   `json:"idle"`
		Limit     int   `json:"limit"`
		WaitCount int64 `json:"wait_count"`
		WaitMS    int64 `json:"wait_ms"`
	} `json:"database"`
	Alerts []OperationalAlert `json:"alerts"`
}

func budgetNearLimit(used, reserved string, limit *string) bool {
	if limit == nil {
		return false
	}
	bound, err := evaluationMoney(*limit)
	if err != nil {
		return false
	}
	if bound.Sign() == 0 {
		return true
	}
	a, err := evaluationMoney(used)
	if err != nil {
		return false
	}
	b, err := evaluationMoney(reserved)
	if err != nil {
		return false
	}
	return new(big.Int).Mul(new(big.Int).Add(a, b), big.NewInt(5)).Cmp(new(big.Int).Mul(bound, big.NewInt(4))) >= 0
}
func (s *Server) operations(w http.ResponseWriter, r *http.Request) error {
	out := OperationsReport{UptimeSeconds: int64(time.Since(s.startedAt).Seconds()), Runtime: s.Runtime, ModelInFlight: len(s.Engine.slots), TrialInFlight: len(s.trialSlots), Alerts: []OperationalAlert{}}
	s.admission.mu.Lock()
	out.ActiveRequests, out.RequestBodyBytes = s.admission.active, s.admission.bytes
	s.admission.mu.Unlock()
	if err := s.Store.DB.QueryRowContext(r.Context(), "SELECT COALESCE(MAX(version),0) FROM audit_schema_migrations").Scan(&out.MigrationVersion); err != nil {
		return err
	}
	if err := s.Store.DB.QueryRowContext(r.Context(), "SELECT COUNT(*) FILTER(WHERE status='pending'),COUNT(*) FILTER(WHERE status='estimated') FROM audit_costs").Scan(&out.PendingCosts, &out.EstimatedCosts); err != nil {
		return err
	}
	if err := s.Store.DB.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM evaluation_runs WHERE status IN ('queued','running')").Scan(&out.RunningEvaluations); err != nil {
		return err
	}
	policies, err := s.Store.Policies(r.Context())
	if err != nil {
		return err
	}
	channels, err := s.Store.Channels(r.Context())
	if err != nil {
		return err
	}
	byID := map[string]ModelChannel{}
	for _, c := range channels {
		c.Health = s.currentChannelHealth(c)
		byID[c.ID] = c
		if !c.Enabled || !c.CredentialActive {
			continue
		}
		if c.Health.Status == "configuration_error" {
			out.Alerts = append(out.Alerts, OperationalAlert{"channel:" + c.ID, "warning", c.Name + "：连接配置异常", "channels"})
		} else if !c.Health.Verified && c.Health.LastFailureAt == nil {
			out.Alerts = append(out.Alerts, OperationalAlert{"unverified:" + c.ID, "info", c.Name + "：尚未完成成功调用", "channels"})
		}
	}
	for _, p := range policies {
		if !p.Enabled {
			continue
		}
		available := false
		for _, binding := range p.Config.Channels {
			c, ok := byID[binding.ChannelID]
			if ok && binding.Enabled && c.Enabled && c.CredentialActive && c.Health.Status != "configuration_error" && c.Health.Status != "cooling" {
				available = true
				break
			}
		}
		if !available {
			out.Alerts = append(out.Alerts, OperationalAlert{"policy:" + p.ID, "critical", p.Name + "：当前没有可调度通道", "channels"})
		}
	}
	if out.PendingCosts > 0 {
		out.Alerts = append(out.Alerts, OperationalAlert{"pending-costs", "warning", "存在待核对费用，相关预算可能持续被占用", "billing"})
	}
	keys, err := s.Store.Keys(r.Context())
	if err != nil {
		return err
	}
	activeKeys := map[string]bool{}
	for _, key := range keys {
		if !key.Active || key.ExpiresAt != nil && !key.ExpiresAt.After(time.Now()) {
			continue
		}
		activeKeys[key.ID] = true
		if key.ExpiresAt != nil && key.ExpiresAt.Before(time.Now().Add(24*time.Hour)) && key.LastUsedAt != nil && key.LastUsedAt.After(time.Now().Add(-7*24*time.Hour)) {
			out.Alerts = append(out.Alerts, OperationalAlert{"expiry:" + key.ID, "warning", key.Name + "：访问密钥将在 24 小时内到期", "keys"})
		}
	}
	budgets, err := s.Store.Budgets(r.Context(), time.Now())
	if err != nil {
		return err
	}
	for _, budget := range budgets {
		if activeKeys[budget.ClientID] && (budgetNearLimit(budget.DailyUsed, budget.DailyReserved, budget.DailyLimit) || budgetNearLimit(budget.MonthlyUsed, budget.MonthlyReserved, budget.MonthlyLimit)) {
			out.Alerts = append(out.Alerts, OperationalAlert{"budget:" + budget.ClientID, "warning", budget.Name + "：预算不可用或日/月占用已达到 80%", "billing"})
		}
	}
	stats := s.Store.DB.Stats()
	out.Database.Open, out.Database.InUse, out.Database.Idle, out.Database.Limit = stats.OpenConnections, stats.InUse, stats.Idle, stats.MaxOpenConnections
	out.Database.WaitCount, out.Database.WaitMS = stats.WaitCount, stats.WaitDuration.Milliseconds()
	if out.ModelInFlight >= s.Runtime.ModelConcurrency {
		out.Alerts = append(out.Alerts, OperationalAlert{"model-capacity", "warning", "上游模型并发已满", "channels"})
	}
	if out.ActiveRequests >= s.Runtime.RequestConcurrency {
		out.Alerts = append(out.Alerts, OperationalAlert{"request-capacity", "warning", "在途请求并发已满", "logs"})
	}
	levels := map[string]int{"critical": 0, "warning": 1, "info": 2}
	sort.SliceStable(out.Alerts, func(i, j int) bool { return levels[out.Alerts[i].Severity] < levels[out.Alerts[j].Severity] })
	return writeJSON(w, 200, out)
}
