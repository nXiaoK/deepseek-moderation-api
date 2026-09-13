package audit

// Stock sub2api ignores flagged and audit, and compares only its built-in
// category_scores with local thresholds. Use illicit as a compatibility carrier
// for our policy verdict, not as a claim about the model's classification.
// Binary scores preserve our threshold decision for sub2api thresholds in (0,1].
// The original model score remains in audit.confidence and our audit log.
func moderationResult(p Policy, assessment Assessment) Result {
	flagged := assessment.Confidence >= p.Config.Threshold
	score := 0.0
	if flagged {
		score = 1
	}
	return Result{
		Flagged:    flagged,
		Categories: map[string]bool{"illicit": flagged},
		Scores:     map[string]float64{"illicit": score},
		Audit: AuditMetadata{
			SchemaVersion: 1,
			PolicyID:      p.ID,
			PolicyVersion: int(p.Revision),
			Confidence:    assessment.Confidence,
			Threshold:     p.Config.Threshold,
			Reason:        redactReason(assessment.Reason),
		},
	}
}
