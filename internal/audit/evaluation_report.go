package audit

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"math/big"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

type EvaluationScore struct {
	Target           string   `json:"target"`
	Total            int      `json:"total"`
	Processed        int      `json:"processed"`
	Valid            int      `json:"valid"`
	Errors           int      `json:"errors"`
	Labelled         int      `json:"labelled"`
	Correct          int      `json:"correct"`
	AllowSamples     int      `json:"allow_samples"`
	FlaggedSamples   int      `json:"flagged_samples"`
	FalsePositives   int      `json:"false_positives"`
	FalseNegatives   int      `json:"false_negatives"`
	UnstableSamples  int      `json:"unstable_samples"`
	AverageLatencyMS *float64 `json:"average_latency_ms"`
	KnownCostCNY     string   `json:"known_cost_cny"`
	PendingCosts     int      `json:"pending_costs"`
}

func (s *Store) evaluationResults(ctx context.Context, id string) ([]EvaluationResult, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT sequence,sample_id,sample_name,target,iteration,expected,status,payload FROM evaluation_results WHERE run_id=$1 ORDER BY sequence", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []EvaluationResult{}
	ids := []string{}
	for rows.Next() {
		var item EvaluationResult
		var raw []byte
		if err := rows.Scan(&item.Sequence, &item.SampleID, &item.SampleName, &item.Target, &item.Iteration, &item.Expected, &item.Status, &raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, err
		}
		if item.RequestID != "" {
			ids = append(ids, item.RequestID)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	costs, err := s.requestCosts(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range items {
		if items[i].RequestID != "" {
			items[i].Cost = costs[items[i].RequestID]
		}
	}
	return items, nil
}
func evaluationScores(results []EvaluationResult) ([]EvaluationScore, error) {
	type accumulator struct {
		score   EvaluationScore
		cost    *big.Int
		latency int64
		seen    map[string]int
	}
	groups := map[string]*accumulator{}
	for _, result := range results {
		group := groups[result.Target]
		if group == nil {
			group = &accumulator{score: EvaluationScore{Target: result.Target}, cost: new(big.Int), seen: map[string]int{}}
			groups[result.Target] = group
		}
		group.score.Total++
		if result.Cost != nil {
			if result.Cost.AmountCNY == nil {
				group.score.PendingCosts++
			} else {
				amount, err := evaluationMoney(*result.Cost.AmountCNY)
				if err != nil {
					return nil, err
				}
				group.cost.Add(group.cost, amount)
			}
		}
		if result.Status == "pending" || result.Status == "running" || result.Status == "skipped" {
			continue
		}
		group.score.Processed++
		group.latency += result.LatencyMS
		if result.Status != "completed" || result.Confidence == nil {
			group.score.Errors++
			continue
		}
		group.score.Valid++
		if result.Flagged {
			group.seen[result.SampleID] |= 2
		} else {
			group.seen[result.SampleID] |= 1
		}
		switch result.Expected {
		case "allow":
			group.score.Labelled++
			group.score.AllowSamples++
			if result.Flagged {
				group.score.FalsePositives++
			} else {
				group.score.Correct++
			}
		case "flagged":
			group.score.Labelled++
			group.score.FlaggedSamples++
			if !result.Flagged {
				group.score.FalseNegatives++
			} else {
				group.score.Correct++
			}
		}
	}
	scores := []EvaluationScore{}
	for _, group := range groups {
		group.score.KnownCostCNY = evaluationMoneyString(group.cost)
		if group.score.Processed > 0 {
			avg := float64(group.latency) / float64(group.score.Processed)
			group.score.AverageLatencyMS = &avg
		}
		for _, mask := range group.seen {
			if mask == 3 {
				group.score.UnstableSamples++
			}
		}
		scores = append(scores, group.score)
	}
	sort.Slice(scores, func(i, j int) bool { return scores[i].Target < scores[j].Target })
	return scores, nil
}
func (s *Server) evaluationRunDetail(w http.ResponseWriter, r *http.Request) error {
	run, plan, err := s.Store.evaluationRun(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	results, err := s.Store.evaluationResults(r.Context(), run.ID)
	if err != nil {
		return err
	}
	scores, err := evaluationScores(results)
	if err != nil {
		return err
	}
	targets := map[string]string{"": "按策略调度"}
	for _, c := range plan.Channels {
		targets[c.ID] = c.Name + " · " + c.Model
	}
	return writeJSON(w, 200, map[string]any{"run": run, "config": plan.Policy.Config, "policy_revision": plan.Policy.Revision, "targets": targets, "results": results, "scores": scores})
}
func (s *Server) evaluationRunSample(w http.ResponseWriter, r *http.Request) error {
	_, plan, err := s.Store.evaluationRun(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	for _, sample := range plan.Samples {
		if sample.ID == r.PathValue("sample") {
			return writeJSON(w, 200, sample)
		}
	}
	return ErrNotFound
}
func csvCell(value string) string {
	trimmed := strings.TrimLeft(value, " \t\r\n")
	if len(trimmed) > 0 && strings.ContainsRune("=+-@", rune(trimmed[0])) {
		return "'" + value
	}
	return value
}
func (s *Server) exportEvaluation(w http.ResponseWriter, r *http.Request) error {
	run, plan, err := s.Store.evaluationRun(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	results, err := s.Store.evaluationResults(r.Context(), run.ID)
	if err != nil {
		return err
	}
	targets := map[string]string{"": "按策略调度"}
	for _, c := range plan.Channels {
		targets[c.ID] = c.Name + " / " + c.Model
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="evaluation-`+run.ID+`.csv"`)
	if _, err := w.Write([]byte{0xef, 0xbb, 0xbf}); err != nil {
		return err
	}
	out := csv.NewWriter(w)
	if err := out.Write([]string{"样本", "通道", "轮次", "预期", "状态", "实际命中", "评分", "阈值", "耗时(ms)", "已知费用(CNY)", "请求ID", "错误码"}); err != nil {
		return err
	}
	for _, row := range results {
		confidence, flagged, cost := "", "", ""
		if row.Confidence != nil {
			confidence = strconv.FormatFloat(*row.Confidence, 'g', -1, 64)
			flagged = strconv.FormatBool(row.Flagged)
		}
		if row.Cost != nil {
			if row.Cost.AmountCNY == nil {
				cost = "待核对"
			} else {
				cost = *row.Cost.AmountCNY
			}
		}
		cells := []string{row.SampleName, targets[row.Target], strconv.Itoa(row.Iteration), row.Expected, row.Status, flagged, confidence, strconv.FormatFloat(row.Threshold, 'g', -1, 64), strconv.FormatInt(row.LatencyMS, 10), cost, row.RequestID, row.ErrorCode}
		for i := range cells {
			cells[i] = csvCell(cells[i])
		}
		if err := out.Write(cells); err != nil {
			return err
		}
	}
	out.Flush()
	return out.Error()
}
