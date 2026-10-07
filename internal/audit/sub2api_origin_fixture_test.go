package audit

// SPDX-License-Identifier: LGPL-3.0-only
// Frozen protocol fixture from Wei-Shaw/sub2api v0.2.14, origin/main
// 3f1a2ea0a760730e3bc528105c00b4ee4f23e469:
// backend/internal/service/content_moderation.go and
// backend/internal/service/content_moderation_engines.go. These are the
// unmodified response decoder types, engine metadata, default thresholds and
// decision function. Keeping this independent of our response builder catches
// wire compatibility bugs. Only used in tests; no sub2api runtime dependency
// or project changes. Upstream license: testdata/sub2api-LICENSE.txt.

var contentModerationCategoryOrder = []string{
	"harassment",
	"harassment/threatening",
	"hate",
	"hate/threatening",
	"illicit",
	"illicit/violent",
	"self-harm",
	"self-harm/intent",
	"self-harm/instructions",
	"sexual",
	"sexual/minors",
	"violence",
	"violence/graphic",
}

func ContentModerationDefaultThresholds() map[string]float64 {
	return map[string]float64{
		"harassment":             0.98,
		"harassment/threatening": 0.90,
		"hate":                   0.65,
		"hate/threatening":       0.65,
		"illicit":                0.95,
		"illicit/violent":        0.95,
		"self-harm":              0.65,
		"self-harm/intent":       0.85,
		"self-harm/instructions": 0.65,
		"sexual":                 0.65,
		"sexual/minors":          0.65,
		"violence":               0.95,
		"violence/graphic":       0.95,
	}
}

type ContentModerationEngineMeta struct {
	Engine        string `json:"engine"`
	Model         string `json:"model"`
	RulesVersion  string `json:"rules_version"`
	SkippedImages int    `json:"skipped_images"`
}

type moderationAPIResponse struct {
	Model   string                `json:"model"`
	Results []moderationAPIResult `json:"results"`
}

type moderationAPIResult struct {
	EngineMeta     *ContentModerationEngineMeta `json:"-"`
	Flagged        bool                         `json:"flagged"`
	CategoryScores map[string]float64           `json:"category_scores"`
}

func evaluateModerationScores(scores map[string]float64, thresholds map[string]float64) (bool, string, float64) {
	flagged := false
	highestCategory := ""
	highestScore := 0.0
	for _, category := range contentModerationCategoryOrder {
		score := scores[category]
		if score > highestScore || highestCategory == "" {
			highestScore = score
			highestCategory = category
		}
		if score >= thresholds[category] {
			flagged = true
		}
	}
	for category, score := range scores {
		if score > highestScore || highestCategory == "" {
			highestScore = score
			highestCategory = category
		}
	}
	return flagged, highestCategory, highestScore
}
