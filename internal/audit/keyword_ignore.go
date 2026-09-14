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

// A bypass has no billing rows. Derive its zero cost when reading records too.
func keywordIgnoreCost(cost *CostView, ignored bool) *CostView {
	if cost != nil || !ignored {
		return cost
	}
	zero := "0"
	return &CostView{Status: "zero", AmountCNY: &zero, ReservedCNY: zero, Note: "关键词忽略，未调用模型"}
}
