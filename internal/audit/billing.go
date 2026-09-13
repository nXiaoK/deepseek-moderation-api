package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type CostReservation struct {
	ImageInput                   bool
	GatewayManaged               bool
	Provider                     string
	ID, ClientID, Kind, PolicyID string
	Model                        string
	Price                        *PriceCard
	StartedAt                    time.Time
	Reserved                     int64
	CacheHit                     bool
}
type CostRow struct {
	ID        string     `json:"id"`
	ChannelID string     `json:"channel_id"`
	Price     *PriceCard `json:"price_snapshot,omitempty"`
	RequestID string     `json:"request_id"`
	ClientID  string     `json:"client_id"`
	Kind      string     `json:"kind"`
	PolicyID  string     `json:"policy_id"`
	Model     string     `json:"model"`
	StartedAt time.Time  `json:"started_at"`
	Usage     Usage      `json:"usage"`
	Cost      CostView   `json:"cost"`
}
type BudgetView struct {
	ClientID        string  `json:"client_id"`
	Name            string  `json:"name"`
	Revision        int64   `json:"revision"`
	DailyLimit      *string `json:"daily_limit_cny"`
	MonthlyLimit    *string `json:"monthly_limit_cny"`
	DailyUsed       string  `json:"daily_used_cny"`
	DailyReserved   string  `json:"daily_reserved_cny"`
	MonthlyUsed     string  `json:"monthly_used_cny"`
	MonthlyReserved string  `json:"monthly_reserved_cny"`
}

func moneyPtr(v sql.NullInt64) *string {
	if !v.Valid {
		return nil
	}
	s := picoString(v.Int64)
	return &s
}
func (s *Store) Price(ctx context.Context, model string, at time.Time) (*PriceCard, error) {
	var c PriceCard
	var raw []byte
	err := s.DB.QueryRowContext(ctx, "SELECT id,model,rates,source,effective_at FROM model_prices WHERE model=$1 AND effective_at<=$2 ORDER BY effective_at DESC,id DESC LIMIT 1", canonicalPriceModel(model), at).Scan(&c.ID, &c.Model, &raw, &c.Source, &c.EffectiveAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(raw, &c.Rates); err != nil {
		return nil, err
	}
	return &c, c.Rates.Validate()
}
func (s *Store) Prices(ctx context.Context) ([]PriceCard, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT DISTINCT ON(model) id,model,rates,source,effective_at FROM model_prices WHERE effective_at<=NOW() ORDER BY model,effective_at DESC,id DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []PriceCard{}
	for rows.Next() {
		var c PriceCard
		var raw []byte
		if err = rows.Scan(&c.ID, &c.Model, &raw, &c.Source, &c.EffectiveAt); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &c.Rates); err != nil {
			return nil, err
		}
		items = append(items, c)
	}
	return items, rows.Err()
}
func (s *Store) ReserveCost(ctx context.Context, id, client, kind string, p Policy, cfg PolicyConfig, text string, cached bool, at time.Time, route ...string) (*CostReservation, error) {
	requestID, channelID := id, ""
	if len(route) == 2 {
		requestID, channelID = route[0], route[1]
	}
	card, err := s.Price(ctx, cfg.Model, at)
	if !cfg.officialPricing() {
		card = nil
	}
	if err != nil {
		return nil, err
	}
	reserved := int64(0)
	if card != nil && !cached && cfg.ImageCount == 0 {
		reserved, err = card.reserve(cfg, text)
		if err != nil {
			return nil, err
		}
	}
	entry := &CostReservation{ImageInput: cfg.ImageCount > 0, GatewayManaged: !cfg.officialPricing(), Provider: cfg.ProviderID(), ID: id, ClientID: client, Kind: kind, PolicyID: p.ID, Model: cfg.Model, Price: card, StartedAt: at, Reserved: reserved, CacheHit: cached}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	date := at.In(shanghai).Format("2006-01-02")
	month := at.In(shanghai).Format("2006-01") + "-01"
	if kind == "production" && !cached {
		var active bool
		if err = tx.QueryRowContext(ctx, "SELECT active FROM client_api_keys WHERE id=$1 FOR UPDATE", client).Scan(&active); err != nil {
			return nil, err
		}
		if !active {
			return nil, problem(401, "invalid_api_key", "访问密钥已停用")
		}
		var dayLimit, monthLimit sql.NullInt64
		err = tx.QueryRowContext(ctx, "SELECT daily_limit,monthly_limit FROM client_budgets WHERE client_id=$1", client).Scan(&dayLimit, &monthLimit)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if dayLimit.Valid || monthLimit.Valid {
			if cfg.ImageCount > 0 {
				return nil, problem(503, "pricing_unavailable", "图片用量暂不支持预估，无法在预算内发起审核")
			}
			if card == nil {
				return nil, problem(503, "pricing_unavailable", "该模型未配置单价，无法在预算内发起审核")
			}
			var dayUsed, monthUsed int64
			var unknown int
			err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(COALESCE(amount_pico,reserved_pico)) FILTER(WHERE budget_date=$2::date),0),COALESCE(SUM(COALESCE(amount_pico,reserved_pico)),0),COUNT(*) FILTER(WHERE amount_pico IS NULL AND price_id IS NULL) FROM audit_costs WHERE client_id=$1 AND kind='production' AND budget_date >= $3::date AND budget_date < $3::date+INTERVAL '1 month'`, client, date, month).Scan(&dayUsed, &monthUsed, &unknown)
			if err != nil {
				return nil, err
			}
			if unknown > 0 {
				return nil, problem(429, "budget_pending", "存在无法预估的待核对费用，请先核对后继续使用预算")
			}
			if dayLimit.Valid && (dayUsed > dayLimit.Int64 || reserved > dayLimit.Int64-dayUsed) || monthLimit.Valid && (monthUsed > monthLimit.Int64 || reserved > monthLimit.Int64-monthUsed) {
				return nil, problem(429, "budget_exceeded", "剩余预算不足以预留本次审核的最大估算费用")
			}
		}
	}
	var priceID any
	if card != nil {
		priceID = card.ID
	}
	snapshot, _ := json.Marshal(card)
	status := "reserved"
	var amount any
	if cached {
		status = "local_cache"
		amount = int64(0)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_costs(id,client_id,kind,policy_id,request_id,model,price_id,price_snapshot,started_at,budget_date,reserved_pico,amount_pico,status,tariff_period,provider,channel_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`, id, client, kind, p.ID, requestID, cfg.Model, priceID, string(snapshot), at, date, reserved, amount, status, providerTariff(cfg, at), cfg.ProviderID(), channelID)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return entry, nil
}
func (s *Store) SettleCost(ctx context.Context, entry *CostReservation, u Usage, end time.Time) (*CostView, error) {
	amount := int64(0)
	status, note := "zero", "未发起上游请求，无新增模型费用"
	var amountArg any = int64(0)
	if entry.CacheHit {
		status = "local_cache"
		note = "命中本地精确结果缓存，无新增上游 token"
	} else if u.Attempted {
		if entry.Price == nil {
			status = "pending"
			note = "模型单价未知或使用第三方接口，请核对供应商账单；不套用 DeepSeek 官方价格"
			if entry.Provider == ProviderGrok {
				note = "Grok 模型由 sub2api API 提供；未取得逐请求实际货币成本，不能按 DeepSeek 价格计算"
			}
		} else {
			var err error
			amount, status, note, err = entry.Price.calculate(u, entry.StartedAt, end)
			if err != nil {
				status = "pending"
				note = "用量数值无效，请核对供应商账单"
			}
		}
		if status == "pending" {
			amountArg = nil
		} else {
			amountArg = amount
			if amount > entry.Reserved && !entry.ImageInput {
				note += "；实际计算费用高于预留，请检查预算及估算"
			}
			if entry.ImageInput {
				note += "；图片请求按上游报告用量结算，未预估图片费用"
			}
		}
	}
	period := pricePeriod(entry.StartedAt)
	if entry.GatewayManaged {
		period = "gateway_managed"
	}
	if u.Attempted && entry.Price != nil && pricePeriod(entry.StartedAt) != pricePeriod(end) {
		period = "higher_rate_estimate"
	}
	raw, _ := json.Marshal(u)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if entry.Kind == "production" {
		if _, err = tx.ExecContext(ctx, "SELECT id FROM client_api_keys WHERE id=$1 FOR UPDATE", entry.ClientID); err != nil {
			return nil, err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE audit_costs SET amount_pico=$1,status=$2,usage=$3,settled_at=$4,note=$5,tariff_period=$7 WHERE id=$6 AND status IN ('reserved','local_cache')`, amountArg, status, string(raw), end, note, entry.ID, period)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return s.Cost(ctx, entry.ID)
}
func (s *Store) Cost(ctx context.Context, id string) (*CostView, error) {
	var view CostView
	var amount, priceID sql.NullInt64
	var reserved int64
	var at time.Time
	err := s.DB.QueryRowContext(ctx, "SELECT status,amount_pico,reserved_pico,price_id,started_at,note,tariff_period FROM audit_costs WHERE id=$1", id).Scan(&view.Status, &amount, &reserved, &priceID, &at, &view.Note, &view.Period)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	view.AmountCNY = moneyPtr(amount)
	if !amount.Valid {
		view.ReservedCNY = picoString(reserved)
	} else {
		view.ReservedCNY = "0"
	}
	if priceID.Valid {
		view.PriceID = &priceID.Int64
	}
	if view.Period == "" {
		view.Period = pricePeriod(at)
	}
	return &view, nil
}
func (s *Store) Budgets(ctx context.Context, at time.Time) ([]BudgetView, error) {
	date := at.In(shanghai).Format("2006-01-02")
	month := at.In(shanghai).Format("2006-01") + "-01"
	rows, err := s.DB.QueryContext(ctx, `SELECT k.id,k.name,COALESCE(b.revision,0),b.daily_limit,b.monthly_limit,
 COALESCE(SUM(c.amount_pico) FILTER(WHERE c.budget_date=$1::date),0),COALESCE(SUM(c.reserved_pico) FILTER(WHERE c.budget_date=$1::date AND c.amount_pico IS NULL),0),
 COALESCE(SUM(c.amount_pico),0),COALESCE(SUM(c.reserved_pico) FILTER(WHERE c.amount_pico IS NULL),0)
 FROM client_api_keys k LEFT JOIN client_budgets b ON b.client_id=k.id
 LEFT JOIN audit_costs c ON c.client_id=k.id AND c.kind='production' AND c.budget_date >= $2::date AND c.budget_date < $2::date+INTERVAL '1 month'
 WHERE k.deleted_at IS NULL
 GROUP BY k.id,k.name,b.revision,b.daily_limit,b.monthly_limit ORDER BY k.name`, date, month)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []BudgetView{}
	for rows.Next() {
		var v BudgetView
		var day, month sql.NullInt64
		var usedD, heldD, usedM, heldM int64
		if err = rows.Scan(&v.ClientID, &v.Name, &v.Revision, &day, &month, &usedD, &heldD, &usedM, &heldM); err != nil {
			return nil, err
		}
		v.DailyLimit = moneyPtr(day)
		v.MonthlyLimit = moneyPtr(month)
		v.DailyUsed = picoString(usedD)
		v.DailyReserved = picoString(heldD)
		v.MonthlyUsed = picoString(usedM)
		v.MonthlyReserved = picoString(heldM)
		items = append(items, v)
	}
	return items, rows.Err()
}
func (s *Server) budgets(w http.ResponseWriter, r *http.Request) error {
	items, err := s.Store.Budgets(r.Context(), time.Now())
	if err != nil {
		return err
	}
	return writeJSON(w, 200, items)
}
func (s *Server) saveBudget(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Daily    *string `json:"daily_limit_cny"`
		Monthly  *string `json:"monthly_limit_cny"`
		Revision int64   `json:"expected_revision"`
	}
	if err := readJSON(w, r, &in); err != nil {
		return err
	}
	var day, month any
	for i, p := range []*string{in.Daily, in.Monthly} {
		if p != nil {
			n, err := parseCNY(*p)
			if err != nil {
				return problem(400, "invalid_budget", err.Error())
			}
			if i == 0 {
				day = n
			} else {
				month = n
			}
		}
	}
	id := r.PathValue("id")
	err := s.Store.mutate(r.Context(), actor(r), "budget.update", id, func(tx *sql.Tx) error {
		var exists string
		if err := tx.QueryRowContext(r.Context(), "SELECT id FROM client_api_keys WHERE id=$1 AND deleted_at IS NULL FOR UPDATE", id).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		var revision int64
		err := tx.QueryRowContext(r.Context(), "SELECT revision FROM client_budgets WHERE client_id=$1", id).Scan(&revision)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if revision != in.Revision {
			return ErrConflict
		}
		_, err = tx.ExecContext(r.Context(), `INSERT INTO client_budgets(client_id,daily_limit,monthly_limit) VALUES($1,$2,$3) ON CONFLICT(client_id) DO UPDATE SET daily_limit=$2,monthly_limit=$3,revision=client_budgets.revision+1`, id, day, month)
		return err
	})
	if err != nil {
		return err
	}
	return writeJSON(w, 200, map[string]bool{"ok": true})
}
func (s *Server) prices(w http.ResponseWriter, r *http.Request) error {
	cards, err := s.Store.Prices(r.Context())
	if err != nil {
		return err
	}
	return writeJSON(w, 200, cards)
}
func (s *Server) savePrice(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Model      string            `json:"model"`
		Rates      map[string]string `json:"rates_cny"`
		Source     string            `json:"source"`
		ExpectedID int64             `json:"expected_price_id"`
	}
	if err := readJSON(w, r, &in); err != nil {
		return err
	}
	in.Model = canonicalPriceModel(in.Model)
	if !regexp.MustCompile(`^[a-zA-Z0-9._:/-]{1,100}$`).MatchString(in.Model) || len(in.Source) > 500 || strings.TrimSpace(in.Source) == "" {
		return problem(400, "invalid_price", "请输入模型名称和价格来源")
	}
	rates := PriceRates{}
	targets := map[string]*int64{"off_hit": &rates.OffHit, "off_miss": &rates.OffMiss, "off_output": &rates.OffOutput, "peak_hit": &rates.PeakHit, "peak_miss": &rates.PeakMiss, "peak_output": &rates.PeakOutput}
	if len(in.Rates) != len(targets) {
		return problem(400, "invalid_price", "需要完整的六项单价")
	}
	for key, target := range targets {
		value, ok := in.Rates[key]
		if !ok {
			return problem(400, "invalid_price", "缺少单价")
		}
		pico, err := parseCNY(value)
		if err != nil || pico%1_000_000 != 0 {
			return problem(400, "invalid_price", "每百万 token 单价最多 6 位小数")
		}
		*target = pico / 1_000_000
	}
	if err := rates.Validate(); err != nil {
		return problem(400, "invalid_price", err.Error())
	}
	raw, _ := json.Marshal(rates)
	err := s.Store.mutate(r.Context(), actor(r), "price.publish", in.Model, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(r.Context(), "SELECT pg_advisory_xact_lock(846274903)"); err != nil {
			return err
		}
		var current int64
		err := tx.QueryRowContext(r.Context(), "SELECT id FROM model_prices WHERE model=$1 AND effective_at<=NOW() ORDER BY effective_at DESC,id DESC LIMIT 1", in.Model).Scan(&current)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if current != in.ExpectedID {
			return ErrConflict
		}
		_, err = tx.ExecContext(r.Context(), "INSERT INTO model_prices(model,rates,source,author) VALUES($1,$2,$3,$4)", in.Model, string(raw), in.Source, actor(r))
		return err
	})
	if err != nil {
		return err
	}
	return s.prices(w, r)
}
func costWhere(r *http.Request) (string, []any, error) {
	args := []any{}
	where := []string{"TRUE"}
	add := func(expr string, value any) {
		args = append(args, value)
		where = append(where, fmt.Sprintf(expr, len(args)))
	}
	for _, key := range []string{"kind", "client_id", "model", "status"} {
		if v := r.URL.Query().Get(key); v != "" {
			add(key+"=$%d", v)
		}
	}
	for _, key := range []string{"from", "to"} {
		if v := r.URL.Query().Get(key); v != "" {
			if _, err := time.Parse(time.RFC3339, v); err != nil {
				return "", nil, problem(400, "invalid_date", "时间必须为 RFC3339")
			}
			op := ">="
			if key == "to" {
				op = "<="
			}
			add("started_at"+op+"$%d::timestamptz", v)
		}
	}
	return strings.Join(where, " AND "), args, nil
}
func (s *Server) costs(w http.ResponseWriter, r *http.Request) error {
	where, args, err := costWhere(r)
	if err != nil {
		return err
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	if page > 1000000 {
		return problem(400, "invalid_page", "页码过大")
	}
	var total int
	var computed, estimated, held int64
	var pending, cached int
	var hits, misses, outputs int64
	err = s.Store.DB.QueryRowContext(r.Context(), `SELECT COUNT(*),COALESCE(SUM(amount_pico) FILTER(WHERE status IN ('calculated','reconciled')),0),COALESCE(SUM(amount_pico) FILTER(WHERE status='estimated'),0),COALESCE(SUM(reserved_pico) FILTER(WHERE amount_pico IS NULL),0),COUNT(*) FILTER(WHERE amount_pico IS NULL),COUNT(*) FILTER(WHERE status='local_cache'),COALESCE(SUM((usage->>'prompt_cache_hit_tokens')::bigint),0),COALESCE(SUM((usage->>'prompt_cache_miss_tokens')::bigint),0),COALESCE(SUM((usage->>'completion_tokens')::bigint),0) FROM audit_costs WHERE `+where, args...).Scan(&total, &computed, &estimated, &held, &pending, &cached, &hits, &misses, &outputs)
	if err != nil {
		return err
	}
	args = append(args, 20, (page-1)*20)
	rows, err := s.Store.DB.QueryContext(r.Context(), `SELECT id,request_id,channel_id,client_id,kind,policy_id,model,started_at,status,amount_pico,reserved_pico,price_id,usage,note,price_snapshot,tariff_period FROM audit_costs WHERE `+where+fmt.Sprintf(" ORDER BY started_at DESC,id DESC LIMIT $%d OFFSET $%d", len(args)-1, len(args)), args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []CostRow{}
	for rows.Next() {
		var row CostRow
		var amount, priceID sql.NullInt64
		var reserved int64
		var usage, snapshot []byte
		if err = rows.Scan(&row.ID, &row.RequestID, &row.ChannelID, &row.ClientID, &row.Kind, &row.PolicyID, &row.Model, &row.StartedAt, &row.Cost.Status, &amount, &reserved, &priceID, &usage, &row.Cost.Note, &snapshot, &row.Cost.Period); err != nil {
			return err
		}
		row.Cost.AmountCNY = moneyPtr(amount)
		row.Cost.ReservedCNY = "0"
		if !amount.Valid {
			row.Cost.ReservedCNY = picoString(reserved)
		}
		if priceID.Valid {
			row.Cost.PriceID = &priceID.Int64
		}
		if row.Cost.Period == "" {
			row.Cost.Period = pricePeriod(row.StartedAt)
		}
		if err = json.Unmarshal(usage, &row.Usage); err != nil {
			return err
		}
		if len(snapshot) > 0 {
			if err = json.Unmarshal(snapshot, &row.Price); err != nil {
				return err
			}
		}
		items = append(items, row)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	return writeJSON(w, 200, map[string]any{"items": items, "total": total, "page": page, "summary": map[string]any{"calculated_cny": picoString(computed), "estimated_cny": picoString(estimated), "reserved_cny": picoString(held), "pending_count": pending, "local_cache_hits": cached, "cache_hit_tokens": hits, "cache_miss_tokens": misses, "output_tokens": outputs}})
}
func (s *Server) reconcileCost(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Amount string `json:"amount_cny"`
		Reason string `json:"reason"`
	}
	if err := readJSON(w, r, &in); err != nil {
		return err
	}
	amount, err := parseCNY(in.Amount)
	if err != nil {
		return problem(400, "invalid_amount", err.Error())
	}
	if strings.TrimSpace(in.Reason) == "" || len(in.Reason) > 500 {
		return problem(400, "reason_required", "请填写费用核对依据")
	}
	id := r.PathValue("id")
	err = s.Store.mutate(r.Context(), actor(r), "cost.reconcile", id, func(tx *sql.Tx) error {
		var client, kind string
		if err := tx.QueryRowContext(r.Context(), "SELECT client_id,kind FROM audit_costs WHERE id=$1", id).Scan(&client, &kind); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if kind == "production" {
			if _, err := tx.ExecContext(r.Context(), "SELECT id FROM client_api_keys WHERE id=$1 FOR UPDATE", client); err != nil {
				return err
			}
		}
		var status string
		var before sql.NullInt64
		if err := tx.QueryRowContext(r.Context(), "SELECT status,amount_pico FROM audit_costs WHERE id=$1 FOR UPDATE", id).Scan(&status, &before); err != nil {
			return err
		}
		if status != "pending" && status != "estimated" {
			return problem(409, "cost_already_settled", "仅待核对或估算记录可以核对")
		}
		if _, err := tx.ExecContext(r.Context(), "INSERT INTO cost_adjustments(cost_id,previous_status,previous_amount,amount_pico,reason,author) VALUES($1,$2,$3,$4,$5,$6)", id, status, before, amount, in.Reason, actor(r)); err != nil {
			return err
		}
		_, err := tx.ExecContext(r.Context(), "UPDATE audit_costs SET status='reconciled',amount_pico=$1,note=$2,settled_at=NOW() WHERE id=$3", amount, "管理员核对："+in.Reason, id)
		return err
	})
	if err != nil {
		return err
	}
	return writeJSON(w, 200, map[string]bool{"ok": true})
}
