package audit

import (
	"context"
	"fmt"
	"math/big"

	"github.com/lib/pq"
)

type evaluationCost struct {
	known, held               *big.Int
	total, pending, estimated int
}

func (r EvaluationResult) requestIDs() []string {
	if len(r.RequestIDs) > 0 {
		return r.RequestIDs
	}
	if r.RequestID != "" {
		return []string{r.RequestID}
	}
	return nil
}

// Keep known charges and pending reservations separate, including across retries.
func (s *Store) evaluationCosts(ctx context.Context, ids []string) (map[string]evaluationCost, error) {
	costs := make(map[string]evaluationCost, len(ids))
	for _, id := range ids {
		costs[id] = evaluationCost{known: new(big.Int), held: new(big.Int)}
	}
	if len(ids) == 0 {
		return costs, nil
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT request_id,COUNT(*),COUNT(*) FILTER(WHERE amount_pico IS NULL),COUNT(*) FILTER(WHERE status='estimated'),COALESCE(SUM(amount_pico),0)::text,COALESCE(SUM(reserved_pico) FILTER(WHERE amount_pico IS NULL),0)::text FROM audit_costs WHERE request_id=ANY($1) GROUP BY request_id`, pq.Array(ids))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, known, held string
		var cost evaluationCost
		if err := rows.Scan(&id, &cost.total, &cost.pending, &cost.estimated, &known, &held); err != nil {
			return nil, err
		}
		var ok bool
		cost.known, ok = new(big.Int).SetString(known, 10)
		if !ok {
			return nil, fmt.Errorf("invalid evaluation charge")
		}
		cost.held, ok = new(big.Int).SetString(held, 10)
		if !ok {
			return nil, fmt.Errorf("invalid evaluation reservation")
		}
		costs[id] = cost
	}
	return costs, rows.Err()
}

func (r *EvaluationResult) applyCosts(costs map[string]evaluationCost) {
	known, held := new(big.Int), new(big.Int)
	var total, pending, estimated int
	for _, id := range r.requestIDs() {
		cost, ok := costs[id]
		if !ok {
			continue
		}
		known.Add(known, cost.known)
		held.Add(held, cost.held)
		total += cost.total
		pending += cost.pending
		estimated += cost.estimated
	}
	r.KnownCostCNY = evaluationMoneyString(known)
	r.Cost = nil
	if total > 0 {
		amount := r.KnownCostCNY
		r.Cost = &CostView{Status: "calculated", AmountCNY: &amount, ReservedCNY: evaluationMoneyString(held), Period: "multiple_attempts", Note: "含所有重试调用的费用合计"}
		if estimated > 0 {
			r.Cost.Status = "estimated"
		}
		if pending > 0 {
			r.Cost.Status = "pending"
			r.Cost.AmountCNY = nil
			r.Cost.Note = "存在待核对调用；已知费用小计 ¥" + amount
		}
	}
	r.Cost = keywordIgnoreCost(r.Cost, r.KeywordIgnored)
}
