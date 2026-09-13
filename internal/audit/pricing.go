package audit

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"
)

const picoPerCNY int64 = 1_000_000_000_000

var shanghai = func() *time.Location {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		panic(err)
	}
	return loc
}()

// Price rates use micro-CNY per million tokens. Multiplying one token by a
// stored rate yields pico-CNY exactly, without per-request cent rounding.
type PriceRates struct {
	OffHit     int64 `json:"off_hit"`
	OffMiss    int64 `json:"off_miss"`
	OffOutput  int64 `json:"off_output"`
	PeakHit    int64 `json:"peak_hit"`
	PeakMiss   int64 `json:"peak_miss"`
	PeakOutput int64 `json:"peak_output"`
}
type PriceCard struct {
	ID          int64      `json:"id"`
	Model       string     `json:"model"`
	Rates       PriceRates `json:"rates"`
	Source      string     `json:"source"`
	EffectiveAt time.Time  `json:"effective_at"`
}
type CostView struct {
	Status      string  `json:"status"`
	AmountCNY   *string `json:"amount_cny"`
	ReservedCNY string  `json:"reserved_cny"`
	PriceID     *int64  `json:"price_id,omitempty"`
	Period      string  `json:"period"`
	Note        string  `json:"note"`
}

func picoString(v int64) string {
	if v == 0 {
		return "0"
	}
	s := fmt.Sprintf("%d.%012d", v/picoPerCNY, v%picoPerCNY)
	return strings.TrimRight(strings.TrimRight(s, "0"), ".")
}
func parseCNY(s string) (int64, error) {
	s = strings.TrimSpace(s)
	parts := strings.Split(s, ".")
	if len(parts) > 2 || parts[0] == "" {
		return 0, errors.New("金额格式无效")
	}
	for _, part := range parts {
		for _, r := range part {
			if r < '0' || r > '9' {
				return 0, errors.New("金额必须为非负十进制数")
			}
		}
	}
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || whole > 100000 {
		return 0, errors.New("金额上限为 100000 元")
	}
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
	}
	if len(fraction) > 12 {
		return 0, errors.New("金额最多 12 位小数")
	}
	fraction += strings.Repeat("0", 12-len(fraction))
	tail, err := strconv.ParseInt(fraction, 10, 64)
	if err != nil {
		return 0, err
	}
	if whole == 100000 && tail > 0 {
		return 0, errors.New("金额上限为 100000 元")
	}
	return whole*picoPerCNY + tail, nil
}
func canonicalPriceModel(model string) string {
	switch model {
	case "deepseek-v4-flash", "deepseek-v4-flash-vision-exp":
		return "deepseek-flash"
	}
	return model
}
func pricePeriod(t time.Time) string {
	local := t.In(shanghai)
	week := local.Weekday()
	hour := local.Hour()
	if week >= time.Monday && week <= time.Friday && ((hour >= 9 && hour < 12) || (hour >= 14 && hour < 18)) {
		return "peak"
	}
	return "off_peak"
}
func (r PriceRates) values(period string) (int64, int64, int64) {
	if period == "peak" {
		return r.PeakHit, r.PeakMiss, r.PeakOutput
	}
	return r.OffHit, r.OffMiss, r.OffOutput
}
func (r PriceRates) Validate() error {
	for _, v := range []int64{r.OffHit, r.OffMiss, r.OffOutput, r.PeakHit, r.PeakMiss, r.PeakOutput} {
		if v < 0 || v > 1_000_000_000 {
			return errors.New("每百万 token 单价必须在 0～1000 元之间")
		}
	}
	return nil
}
func safeCost(hit, miss, output int, hitRate, missRate, outRate int64) (int64, error) {
	var total int64
	for _, item := range []struct {
		tokens int
		rate   int64
	}{{hit, hitRate}, {miss, missRate}, {output, outRate}} {
		if item.tokens < 0 || item.rate < 0 || item.rate > 0 && int64(item.tokens) > (math.MaxInt64-total)/item.rate {
			return 0, errors.New("用量计费数值越界")
		}
		total += int64(item.tokens) * item.rate
	}
	return total, nil
}
func (c PriceCard) reserve(cfg PolicyConfig, text string) (int64, error) {
	// Conservative byte-based token allowance, including the fixed prompt,
	// user wrapper and a framing allowance. This is not an official tokenizer.
	input := len([]byte(cfg.Prompt)) + len([]byte(text)) + 1024
	return safeCost(0, input, cfg.MaxTokens, 0, max(c.Rates.PeakMiss, c.Rates.OffMiss), max(c.Rates.PeakOutput, c.Rates.OffOutput))
}
func (c PriceCard) calculate(u Usage, start, end time.Time) (int64, string, string, error) {
	if !u.Reported {
		return 0, "pending", "上游未返回可验证的用量，需要核对实际费用", nil
	}
	status, note := "calculated", "根据上游 usage 与价格快照计算，非供应商实扣账单"
	hit, miss := 0, u.PromptTokens
	if u.CacheHitTokens != nil && u.CacheMissTokens != nil {
		hit, miss = *u.CacheHitTokens, *u.CacheMissTokens
	} else {
		status = "estimated"
		note = "未返回缓存拆分，按全部输入未命中估算"
	}
	h, m, o := c.Rates.values(pricePeriod(start))
	if pricePeriod(start) != pricePeriod(end) {
		h = max(c.Rates.PeakHit, c.Rates.OffHit)
		m = max(c.Rates.PeakMiss, c.Rates.OffMiss)
		o = max(c.Rates.PeakOutput, c.Rates.OffOutput)
		status = "estimated"
		note = "请求跨计费时段，按较高单价估算，待账单核对"
	}
	amount, err := safeCost(hit, miss, u.CompletionTokens, h, m, o)
	return amount, status, note, err
}

// Usage is optional provider metadata. Invalid or incomplete accounting data
// must not become an authoritative zero cost, nor invalidate a valid verdict.
func (u *Usage) UnmarshalJSON(raw []byte) error {
	var wire struct {
		UpstreamRequestID string `json:"upstream_request_id"`
		ActualModel       string `json:"actual_model"`
		Prompt            *int   `json:"prompt_tokens"`
		Reported          *bool  `json:"reported"`
		Reasoning         int    `json:"reasoning_tokens"`
		Completion        *int   `json:"completion_tokens"`
		Total             *int   `json:"total_tokens"`
		Hit               *int   `json:"prompt_cache_hit_tokens"`
		Miss              *int   `json:"prompt_cache_miss_tokens"`
		Details           struct {
			Reasoning int `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	}
	*u = Usage{}
	if json.Unmarshal(raw, &wire) != nil {
		return nil
	}
	if wire.Prompt == nil || wire.Completion == nil || wire.Total == nil {
		return nil
	}
	p, c, t := *wire.Prompt, *wire.Completion, *wire.Total
	if p < 0 || c < 0 || t < 0 || p > t || c != t-p {
		return nil
	}
	u.PromptTokens = p
	u.CompletionTokens = c
	u.TotalTokens = t
	u.UpstreamRequestID = wire.UpstreamRequestID
	u.ActualModel = wire.ActualModel
	u.Reported = wire.Reported == nil || *wire.Reported
	if wire.Hit != nil && wire.Miss != nil && *wire.Hit >= 0 && *wire.Miss >= 0 && *wire.Hit <= p && *wire.Miss == p-*wire.Hit {
		u.CacheHitTokens = wire.Hit
		u.CacheMissTokens = wire.Miss
	}
	if wire.Details.Reasoning >= 0 && wire.Details.Reasoning <= c {
		u.ReasoningTokens = wire.Details.Reasoning
		if u.ReasoningTokens == 0 && wire.Reasoning >= 0 && wire.Reasoning <= c {
			u.ReasoningTokens = wire.Reasoning
		}
	}
	return nil
}

func providerTariff(cfg PolicyConfig, t time.Time) string {
	if !cfg.officialPricing() {
		return "gateway_managed"
	}
	return pricePeriod(t)
}
