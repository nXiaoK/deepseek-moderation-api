package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

type EvaluationRun struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Username   string    `json:"username"`
	PolicyID   string    `json:"policy_id"`
	PolicyName string    `json:"policy_name"`
	Status     string    `json:"status"`
	Total      int       `json:"total"`
	Completed  int       `json:"completed"`
	MaxCostCNY string    `json:"max_cost_cny"`
	Message    string    `json:"message"`
	CreatedAt  time.Time `json:"created_at"`
}
type evaluationPlan struct {
	Policy      Policy             `json:"policy"`
	Channels    []ModelChannel     `json:"channels"`
	Samples     []EvaluationSample `json:"samples"`
	Targets     []string           `json:"targets"`
	Repetitions int                `json:"repetitions"`
	MaxCost     int64              `json:"max_cost"`
}
type EvaluationResult struct {
	Sequence     int       `json:"sequence"`
	SampleID     string    `json:"sample_id"`
	SampleName   string    `json:"sample_name"`
	Target       string    `json:"target"`
	Iteration    int       `json:"iteration"`
	Expected     string    `json:"expected"`
	Status       string    `json:"status"`
	RequestID    string    `json:"request_id"`
	Model        string    `json:"model"`
	Confidence   *float64  `json:"confidence"`
	Flagged      bool      `json:"flagged"`
	Threshold    float64   `json:"threshold"`
	Reason       string    `json:"reason"`
	ErrorCode    string    `json:"error_code"`
	ErrorMessage string    `json:"error_message"`
	LatencyMS    int64     `json:"latency_ms"`
	Cost         *CostView `json:"cost,omitempty"`
}

func (s *Server) evaluationRuns(w http.ResponseWriter, r *http.Request) error {
	rows, err := s.Store.DB.QueryContext(r.Context(), "SELECT id,name,username,policy_id,policy_name,status,total,completed,max_cost_pico,message,created_at FROM evaluation_runs WHERE created_at>=NOW()-INTERVAL '90 days' ORDER BY created_at DESC,id DESC LIMIT 50")
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []EvaluationRun{}
	for rows.Next() {
		var item EvaluationRun
		var cost int64
		if err := rows.Scan(&item.ID, &item.Name, &item.Username, &item.PolicyID, &item.PolicyName, &item.Status, &item.Total, &item.Completed, &cost, &item.Message, &item.CreatedAt); err != nil {
			return err
		}
		item.MaxCostCNY = picoString(cost)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return writeJSON(w, 200, items)
}
func (s *Store) evaluationRun(ctx context.Context, id string) (EvaluationRun, evaluationPlan, error) {
	var run EvaluationRun
	var plan evaluationPlan
	var encrypted []byte
	var cost int64
	err := s.DB.QueryRowContext(ctx, "SELECT id,name,username,policy_id,policy_name,status,total,completed,max_cost_pico,message,created_at,plan_cipher FROM evaluation_runs WHERE id=$1 AND created_at>=NOW()-INTERVAL '90 days'", id).Scan(&run.ID, &run.Name, &run.Username, &run.PolicyID, &run.PolicyName, &run.Status, &run.Total, &run.Completed, &cost, &run.Message, &run.CreatedAt, &encrypted)
	if errors.Is(err, sql.ErrNoRows) {
		return run, plan, ErrNotFound
	}
	if err != nil {
		return run, plan, err
	}
	run.MaxCostCNY = picoString(cost)
	raw, err := s.Vault.Open(encrypted, "evaluation-run:"+id)
	if err != nil {
		return run, plan, err
	}
	err = json.Unmarshal([]byte(raw), &plan)
	return run, plan, err
}
func (s *Server) createEvaluationRun(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Name        string          `json:"name"`
		PolicyID    string          `json:"policy_id"`
		Config      *PolicySettings `json:"config"`
		SampleIDs   []string        `json:"sample_ids"`
		Targets     []string        `json:"channel_ids"`
		Repetitions int             `json:"repetitions"`
		MaxCostCNY  string          `json:"max_cost_cny"`
	}
	if err := readJSON(w, r, &in); err != nil {
		return err
	}
	if strings.TrimSpace(in.Name) == "" || len(in.Name) > 200 || len(in.SampleIDs) < 1 || len(in.SampleIDs) > 100 || len(in.Targets) < 1 || len(in.Targets) > 4 || in.Repetitions < 1 || in.Repetitions > 3 || len(in.SampleIDs)*len(in.Targets)*in.Repetitions > 200 {
		return problem(400, "invalid_evaluation", "名称不能为空；每次 1～100 个样本、1～4 个通道、1～3 轮，总次数最多 200")
	}
	maxCost, err := parseCNY(in.MaxCostCNY)
	if err != nil {
		return problem(400, "invalid_budget", err.Error())
	}
	p, channels, err := s.Store.RouteSnapshot(r.Context(), in.PolicyID, in.Config)
	if err != nil {
		return err
	}
	plan := evaluationPlan{Policy: p, Channels: channels, Targets: in.Targets, Repetitions: in.Repetitions, MaxCost: maxCost}
	seen := map[string]bool{}
	inputBytes := 0
	for _, id := range in.SampleIDs {
		if seen[id] {
			return problem(400, "invalid_evaluation", "样本不能重复")
		}
		seen[id] = true
		sample, err := s.Store.EvaluationSample(r.Context(), id)
		if err != nil {
			return err
		}
		inputBytes += len(sample.Input)
		if inputBytes > 1<<20 {
			return problem(413, "evaluation_too_large", "评测样本输入合计最多 1 MiB")
		}
		plan.Samples = append(plan.Samples, sample)
	}
	seen = map[string]bool{}
	for _, target := range in.Targets {
		if seen[target] {
			return problem(400, "invalid_evaluation", "评测通道不能重复")
		}
		seen[target] = true
		if _, err := s.estimateEvaluationTrial(r.Context(), plan, target, plan.Samples[0].Input); err != nil {
			return err
		}
	}
	id := randomToken("eval_")
	raw, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	actorName := actor(r)
	err = s.Store.mutate(r.Context(), actorName, "evaluation.start", id, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(r.Context(), "SELECT pg_advisory_xact_lock(846274906)"); err != nil {
			return err
		}
		var running bool
		if err := tx.QueryRowContext(r.Context(), "SELECT EXISTS(SELECT 1 FROM evaluation_runs WHERE status IN ('queued','running'))").Scan(&running); err != nil {
			return err
		}
		if running {
			return problem(409, "evaluation_running", "已有评测运行中，请等待完成或取消")
		}
		_, err := tx.ExecContext(r.Context(), "INSERT INTO evaluation_runs(id,name,username,policy_id,policy_name,total,max_cost_pico,plan_cipher) VALUES($1,$2,$3,$4,$5,$6,$7,$8)", id, in.Name, actorName, p.ID, p.Name, len(in.SampleIDs)*len(in.Targets)*in.Repetitions, maxCost, s.Store.Vault.Seal(string(raw), "evaluation-run:"+id))
		if err != nil {
			return err
		}
		sequence := 0
		for _, sample := range plan.Samples {
			for _, target := range plan.Targets {
				for iteration := 1; iteration <= plan.Repetitions; iteration++ {
					_, err := tx.ExecContext(r.Context(), "INSERT INTO evaluation_results(run_id,sequence,sample_id,sample_name,target,iteration,expected) VALUES($1,$2,$3,$4,$5,$6,$7)", id, sequence, sample.ID, sample.Name, target, iteration, sample.Expected)
					if err != nil {
						return err
					}
					sequence++
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !s.launchEvaluation(id, actorName, plan) {
		s.finishEvaluation(id, "interrupted", "服务正在停止，评测未开始")
		return problem(503, "evaluation_unavailable", "服务正在停止")
	}
	return writeJSON(w, 201, map[string]string{"id": id})
}
func (s *Server) estimateEvaluationTrial(ctx context.Context, plan evaluationPlan, target, input string) (int64, error) {
	values := []int64{}
	for _, binding := range plan.Policy.Config.Channels {
		if !binding.Enabled || target != "" && binding.ChannelID != target {
			continue
		}
		index := slices.IndexFunc(plan.Channels, func(c ModelChannel) bool { return c.ID == binding.ChannelID && c.Enabled && c.CredentialActive })
		if index < 0 {
			continue
		}
		c := plan.Channels[index]
		card, err := s.Store.ConnectionPrice(ctx, c.Model, c.CredentialID, time.Now())
		if err != nil {
			return 0, err
		}
		if card == nil {
			return 0, problem(400, "pricing_unavailable", "评测涉及的模型通道需要先配置单价")
		}
		amount, err := card.reserve(c.Inference(plan.Policy.Config), input)
		if err != nil {
			return 0, err
		}
		values = append(values, amount)
	}
	if len(values) == 0 {
		return 0, problem(400, "invalid_channel", "请选择策略中可用的评测通道")
	}
	slices.Sort(values)
	slices.Reverse(values)
	limit := plan.Policy.Config.MaxAttempts
	if target != "" {
		limit = 1
	}
	var total int64
	for _, value := range values[:min(limit, len(values))] {
		if value > int64(100000)*picoPerCNY-total {
			return 0, problem(400, "invalid_budget", "单次预估费用过高")
		}
		total += value
	}
	return total, nil
}
func (s *Server) launchEvaluation(id, username string, plan evaluationPlan) bool {
	s.evaluationMu.Lock()
	defer s.evaluationMu.Unlock()
	if s.closing {
		return false
	}
	ctx, cancel := context.WithTimeout(s.backgroundContext, 30*time.Minute)
	if s.evaluationCancels == nil {
		s.evaluationCancels = make(map[string]context.CancelFunc)
	}
	s.evaluationCancels[id] = cancel
	s.evaluationWorkers.Add(1)
	go func() {
		defer s.evaluationWorkers.Done()
		defer func() { cancel(); s.evaluationMu.Lock(); delete(s.evaluationCancels, id); s.evaluationMu.Unlock() }()
		s.executeEvaluation(ctx, id, username, plan)
	}()
	return true
}
func (s *Server) finishEvaluation(id, status, message string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	tx, err := s.Store.DB.BeginTx(ctx, nil)
	if err != nil {
		return
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "UPDATE evaluation_runs SET status=CASE WHEN status='cancelled' THEN status ELSE $2 END,message=$3,finished_at=NOW() WHERE id=$1", id, status, message); err != nil {
		return
	}
	if _, err = tx.ExecContext(ctx, "UPDATE evaluation_results SET status=CASE WHEN status='running' THEN 'interrupted' ELSE 'skipped' END WHERE run_id=$1 AND status IN ('pending','running')", id); err != nil {
		return
	}
	if _, err = tx.ExecContext(ctx, "UPDATE evaluation_runs SET completed=(SELECT COUNT(*) FROM evaluation_results WHERE run_id=$1 AND status IN ('completed','error','interrupted')) WHERE id=$1", id); err != nil {
		return
	}
	_ = tx.Commit()
}
func (s *Server) executeEvaluation(ctx context.Context, id, username string, plan evaluationPlan) {
	if _, err := s.Store.DB.ExecContext(ctx, "UPDATE evaluation_runs SET status='running' WHERE id=$1 AND status='queued'", id); err != nil {
		s.finishEvaluation(id, "interrupted", "评测无法开始")
		return
	}
	spent := new(big.Int)
	sequence := 0
	for _, sample := range plan.Samples {
		for _, target := range plan.Targets {
			for iteration := 1; iteration <= plan.Repetitions; iteration++ {
				if ctx.Err() != nil {
					s.finishEvaluation(id, "interrupted", "评测已停止")
					return
				}
				var status string
				if err := s.Store.DB.QueryRowContext(ctx, "SELECT status FROM evaluation_runs WHERE id=$1", id).Scan(&status); err != nil {
					s.finishEvaluation(id, "interrupted", "评测状态读取失败")
					return
				}
				if status != "running" {
					s.finishEvaluation(id, status, "评测已停止")
					return
				}
				estimate, err := s.estimateEvaluationTrial(ctx, plan, target, sample.Input)
				if err != nil {
					s.finishEvaluation(id, "interrupted", storedAuditError(err))
					return
				}
				if new(big.Int).Add(spent, big.NewInt(estimate)).Cmp(big.NewInt(plan.MaxCost)) > 0 {
					s.finishEvaluation(id, "budget_exhausted", "剩余评测预算不足以预留下一次调用")
					return
				}
				for {
					ok, err := s.Store.Rate(ctx, "evaluation:"+username, 30)
					if err != nil {
						s.finishEvaluation(id, "interrupted", "评测限流状态不可用")
						return
					}
					if ok {
						break
					}
					select {
					case <-ctx.Done():
						s.finishEvaluation(id, "interrupted", "评测已停止")
						return
					case <-time.After(time.Second):
					}
				}
				requestID := randomToken("audit_")
				if _, err := s.Store.DB.ExecContext(ctx, "UPDATE evaluation_results SET status='running',payload=jsonb_build_object('request_id',$3::text) WHERE run_id=$1 AND sequence=$2", id, sequence, requestID); err != nil {
					s.finishEvaluation(id, "interrupted", "评测进度保存失败")
					return
				}
				trace := &requestAudit{days: plan.Policy.Config.RetentionDays, log: AuditLog{ID: requestID, CreatedAt: time.Now().UTC(), Request: &AuditRequest{Method: "POST", Path: "/admin/evaluation/runs/" + id, Stage: "audit", InputType: "text", TextChars: utf8.RuneCountInString(sample.Input)}}}
				callCtx := context.WithValue(ctx, requestAuditKey{}, trace)
				response, callErr := s.runAudit(callCtx, plan.Policy, plan.Channels, username, "test", sample.Input, target)
				result := EvaluationResult{Sequence: sequence, SampleID: sample.ID, SampleName: sample.Name, Target: target, Iteration: iteration, Expected: sample.Expected, Status: "completed", RequestID: response.ID, Model: response.ActualModel, Threshold: plan.Policy.Config.Threshold, LatencyMS: response.LatencyMS, Cost: response.Cost}
				if callErr != nil {
					result.Status = "error"
					result.ErrorCode = errorCode(callErr)
					result.ErrorMessage = storedAuditError(callErr)
				} else if len(response.Results) == 1 {
					verdict := response.Results[0]
					result.Confidence = &verdict.Audit.Confidence
					result.Flagged = verdict.Flagged
					result.Reason = verdict.Audit.Reason
				}
				raw, _ := json.Marshal(result)
				writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				tx, err := s.Store.DB.BeginTx(writeCtx, nil)
				if err == nil {
					_, err = tx.ExecContext(writeCtx, "UPDATE evaluation_results SET status=$3,payload=$4 WHERE run_id=$1 AND sequence=$2", id, sequence, result.Status, string(raw))
					if err == nil {
						_, err = tx.ExecContext(writeCtx, "UPDATE evaluation_runs SET completed=completed+1 WHERE id=$1", id)
					}
					if err == nil {
						err = tx.Commit()
					} else {
						_ = tx.Rollback()
					}
				}
				cancel()
				if err != nil {
					s.finishEvaluation(id, "interrupted", "评测结果保存失败")
					return
				}
				sequence++
				if response.Cost != nil {
					if response.Cost.AmountCNY == nil {
						s.finishEvaluation(id, "pending_cost", "存在待核对费用，已停止后续调用")
						return
					}
					amount, err := evaluationMoney(*response.Cost.AmountCNY)
					if err != nil {
						s.finishEvaluation(id, "interrupted", "费用金额无法解析")
						return
					}
					spent.Add(spent, amount)
					if spent.Cmp(big.NewInt(plan.MaxCost)) > 0 {
						s.finishEvaluation(id, "budget_exhausted", "实际上游费用高于预留预算，已停止后续调用")
						return
					}
				}
				if callErr != nil && (strings.HasPrefix(errorCode(callErr), "cost_") || errorCode(callErr) == "record_unavailable") {
					s.finishEvaluation(id, "interrupted", "记录或结算失败，已停止后续调用")
					return
				}
			}
		}
	}
	s.finishEvaluation(id, "completed", "")
}
func (s *Store) RecoverEvaluations(ctx context.Context) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "UPDATE evaluation_results SET status=CASE WHEN status='running' THEN 'interrupted' ELSE 'skipped' END WHERE status IN ('pending','running') AND run_id IN (SELECT id FROM evaluation_runs WHERE status IN ('queued','running','cancelled'))"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE evaluation_runs SET status='interrupted',message='服务重启，未自动重发调用；中断结果请结合关联费用核对',finished_at=NOW(),completed=(SELECT COUNT(*) FROM evaluation_results WHERE run_id=evaluation_runs.id AND status IN ('completed','error','interrupted')) WHERE status IN ('queued','running')"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE evaluation_runs SET completed=(SELECT COUNT(*) FROM evaluation_results WHERE run_id=evaluation_runs.id AND status IN ('completed','error','interrupted')) WHERE status='cancelled'"); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Server) cancelEvaluation(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	err := s.Store.mutate(r.Context(), actor(r), "evaluation.cancel", id, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(r.Context(), "UPDATE evaluation_runs SET status='cancelled',message='管理员取消评测',finished_at=NOW() WHERE id=$1 AND status IN ('queued','running')", id)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return problem(409, "evaluation_finished", "评测已结束或不存在")
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.evaluationMu.Lock()
	cancel := s.evaluationCancels[id]
	s.evaluationMu.Unlock()
	if cancel != nil {
		cancel()
	} else {
		s.finishEvaluation(id, "cancelled", "管理员取消评测")
	}
	return writeJSON(w, 200, map[string]bool{"ok": true})
}
func evaluationMoney(value string) (*big.Int, error) {
	r, ok := new(big.Rat).SetString(value)
	if !ok || r.Sign() < 0 {
		return nil, fmt.Errorf("invalid evaluation cost")
	}
	r.Mul(r, new(big.Rat).SetInt64(picoPerCNY))
	if !r.IsInt() {
		return nil, fmt.Errorf("invalid evaluation cost precision")
	}
	return new(big.Int).Set(r.Num()), nil
}
func evaluationMoneyString(value *big.Int) string {
	whole, remainder := new(big.Int), new(big.Int)
	whole.QuoRem(value, big.NewInt(picoPerCNY), remainder)
	if remainder.Sign() == 0 {
		return whole.String()
	}
	fraction := remainder.String()
	return whole.String() + "." + strings.TrimRight(strings.Repeat("0", 12-len(fraction))+fraction, "0")
}
