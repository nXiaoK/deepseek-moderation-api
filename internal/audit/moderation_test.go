package audit

import (
	"encoding/json"
	"math"
	"testing"
)

func assertStockSub2APIDecision(t *testing.T, body []byte, want bool) {
	t.Helper()
	var response moderationAPIResponse
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 {
		t.Fatalf("expected one moderation result, got %d", len(response.Results))
	}
	got, _, _ := evaluateModerationScores(response.Results[0].CategoryScores, ContentModerationDefaultThresholds())
	if got != want || response.Results[0].Flagged != want {
		t.Fatalf("stock sub2api decision=%v, flagged=%v, want %v; body=%s", got, response.Results[0].Flagged, want, body)
	}
}

func TestModerationStockSub2APIContract(t *testing.T) {
	for _, tc := range []struct {
		name             string
		score, threshold float64
		want             bool
	}{
		{"reported_95_percent", .95, .8, true},
		{"equality_below_sub2api_default", .8, .8, true},
		{"just_below_boundary", math.Nextafter(.8, 0), .8, false},
		{"raised_policy_threshold", .95, .98, false},
		{"low_policy_threshold", .2, .1, true},
		{"zero_score", 0, .8, false},
		{"zero_policy_threshold", 0, 0, true},
		{"one_policy_threshold", 1, 1, true},
		{"below_one", math.Nextafter(1, 0), 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := Policy{ID: "abuse-default", Alias: "abuse-audit-v1", Revision: 2, Config: PolicySettings{Threshold: tc.threshold}}
			result := moderationResult(p, Assessment{Confidence: tc.score, Reason: "审核原因"})
			body, err := json.Marshal(Response{ID: "audit_test", Model: p.Alias, Results: []Result{result}})
			if err != nil {
				t.Fatal(err)
			}
			assertStockSub2APIDecision(t, body, tc.want)
			var response Response
			if err := json.Unmarshal(body, &response); err != nil {
				t.Fatal(err)
			}
			r := response.Results[0]
			if r.Audit.Confidence != tc.score || r.Audit.Threshold != tc.threshold || r.Audit.Reason != "审核原因" || r.Audit.PolicyID != p.ID || r.Audit.PolicyVersion != 2 || r.Audit.SchemaVersion != 1 {
				t.Fatalf("original assessment metadata changed: %+v", r.Audit)
			}
			if len(r.Categories) != 1 || len(r.Scores) != 1 || r.Categories["illicit"] != tc.want {
				t.Fatalf("unexpected categories: %+v", r)
			}
			for _, threshold := range []float64{.01, .5, .95, 1} {
				thresholds := ContentModerationDefaultThresholds()
				thresholds["illicit"] = threshold
				got, category, score := evaluateModerationScores(r.Scores, thresholds)
				if got != tc.want || (tc.want && (category != "illicit" || score != 1)) {
					t.Fatalf("sub2api threshold %v: got (%v, %q, %v), want hit=%v", threshold, got, category, score, tc.want)
				}
			}
		})
	}
}

func TestStockSub2APIIgnoresCustomPolicyAndFlagged(t *testing.T) {
	// Reproduce the screenshot: old response shows 95% but does not hit.
	body := []byte(`{"results":[{"flagged":true,"category_scores":{"custom_policy":0.95}}]}`)
	var response moderationAPIResponse
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatal(err)
	}
	hit, category, score := evaluateModerationScores(response.Results[0].CategoryScores, ContentModerationDefaultThresholds())
	if hit || category != "custom_policy" || score != .95 {
		t.Fatalf("origin fixture no longer reproduces reported behavior: %v %s %v", hit, category, score)
	}
}

func TestStockSub2APIZeroThresholdAlwaysHits(t *testing.T) {
	// Stock sub2api uses >= even for zero/missing scores. This cannot be fixed
	// by a valid [0,1] response; document positive downstream thresholds.
	for _, category := range contentModerationCategoryOrder {
		thresholds := ContentModerationDefaultThresholds()
		thresholds[category] = 0
		hit, _, _ := evaluateModerationScores(map[string]float64{"illicit": 0}, thresholds)
		if !hit {
			t.Fatalf("zero threshold for %s no longer hits", category)
		}
	}
}
