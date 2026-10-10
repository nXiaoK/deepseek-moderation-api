package audit

import "strings"

func (c PolicySettings) ignoresKeywords(input string) bool {
	if !c.KeywordIgnoreEnabled {
		return false
	}
	for _, keyword := range c.IgnoreKeywords {
		if strings.TrimSpace(keyword) != "" && strings.Contains(input, keyword) {
			return true
		}
	}
	return false
}

// Keyword decisions have no billing rows. Derive zero cost on record reads too.
func keywordRuleCost(cost *CostView, ignored, blocked bool) *CostView {
	if cost != nil || !ignored && !blocked {
		return cost
	}
	note := "关键词忽略，未调用模型"
	if blocked {
		note = "关键词阻止，未调用模型"
	}
	zero := "0"
	return &CostView{Status: "zero", AmountCNY: &zero, ReservedCNY: zero, Note: note}
}
