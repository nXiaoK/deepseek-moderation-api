package audit

import (
	"context"
	"slices"
	"time"
)

type evaluationPacingKey struct{}

// Each evaluation worker runs trials serially. Only its actual sent attempts
// advance this clock; production and manual trials do not use this pacer.
type evaluationPacer map[string]time.Time

func evaluationInterval(rpm int) time.Duration {
	if rpm <= 0 {
		return 0
	}
	return time.Minute/time.Duration(rpm) + 2*time.Second
}

func evaluationChannels(plan evaluationPlan, target string) []ModelChannel {
	var channels []ModelChannel
	for _, c := range plan.Channels {
		if c.Enabled && c.CredentialActive && (target == "" || c.ID == target) && slices.ContainsFunc(plan.Policy.Config.Channels, func(b ChannelBinding) bool {
			return b.Enabled && b.ChannelID == c.ID
		}) {
			channels = append(channels, c)
		}
	}
	return channels
}

func (p evaluationPacer) delay(plan evaluationPlan, target, input string, now time.Time) time.Duration {
	if plan.Policy.Config.ignoresKeywords(input) {
		return 0
	}
	var delay time.Duration
	// Routing may use any enabled fallback. Wait until each previously used
	// candidate is due, so fallback calls also respect their own interval.
	for _, c := range evaluationChannels(plan, target) {
		if last, ok := p[c.ID]; ok && c.RPM > 0 {
			delay = max(delay, last.Add(evaluationInterval(c.RPM)).Sub(now))
		}
	}
	return delay
}

func waitEvaluationDelay(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}

func evaluationTimeout(plan evaluationPlan) time.Duration {
	// Preserve the original execution budget and add the maximum pacing time.
	// Low-RPM runs must not be cut off by the former fixed 30-minute deadline.
	budget := 30 * time.Minute
	for _, target := range plan.Targets {
		var interval time.Duration
		for _, c := range evaluationChannels(plan, target) {
			interval = max(interval, evaluationInterval(c.RPM))
		}
		budget += interval * time.Duration(len(plan.Samples)*plan.Repetitions)
	}
	return budget
}
