package audit

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

func (c PolicySettings) blocksKeywords(input string) bool {
	if !c.KeywordBlockEnabled {
		return false
	}
	for _, keyword := range c.BlockKeywords {
		if strings.TrimSpace(keyword) == "" {
			continue
		}
		if c.KeywordBlockMatchMode == "exact" {
			if input == keyword {
				return true
			}
		} else if strings.Contains(input, keyword) {
			return true
		}
	}
	return false
}

func validateKeywordList(keywords []string, label string) error {
	if len(keywords) > 100 {
		return fmt.Errorf("%s关键词最多 100 条", label)
	}
	total := 0
	for _, keyword := range keywords {
		size := utf8.RuneCountInString(keyword)
		if strings.TrimSpace(keyword) == "" || size > 1000 {
			return fmt.Errorf("%s关键词不能为空或纯空白，每条最多 1000 个字符", label)
		}
		total += size
	}
	if total > 16000 {
		return fmt.Errorf("%s关键词合计最多 16000 个字符", label)
	}
	return nil
}
