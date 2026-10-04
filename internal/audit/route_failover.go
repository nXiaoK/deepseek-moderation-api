package audit

import (
	"context"
	"time"
)

// This state belongs to one request. Channel health and RPM remain shared in
// Engine; budgets only redirect an unsuccessful request, never a valid verdict.
type routeProgress struct {
	attempts      map[int]int
	deadlines     map[int]time.Time
	skipped       map[int]bool
	totalAttempts int
	maxAttempts   int
	deadline      time.Time
}

func newRouteProgress(ctx context.Context, maxAttempts int) *routeProgress {
	deadline, _ := ctx.Deadline()
	return &routeProgress{
		attempts: make(map[int]int), deadlines: make(map[int]time.Time),
		skipped: make(map[int]bool), maxAttempts: maxAttempts, deadline: deadline,
	}
}

// eligible already observes shared cooldown, concurrency and RPM under routeMu.
// Do not sacrifice the last available tier for a backup that cannot be called.
func (p *routeProgress) selectTier(candidates []routeCandidate, eligible []int, settings PolicySettings, now time.Time) ([]int, time.Time) {
	remaining := p.maxAttempts - p.totalAttempts
	for {
		priority := 101
		for _, i := range eligible {
			level := candidates[i].binding.Priority
			if !p.skipped[level] {
				priority = min(priority, level)
			}
		}
		tier := []int{}
		hasLower := false
		for _, i := range eligible {
			level := candidates[i].binding.Priority
			if p.skipped[level] {
				continue
			}
			if level == priority {
				tier = append(tier, i)
			} else if level > priority {
				hasLower = true
			}
		}
		if len(tier) == 0 {
			return nil, time.Time{}
		}
		deadline := p.deadlines[priority]
		if hasLower && (p.attempts[priority] >= settings.maxAttemptsPerPriority() ||
			remaining == 1 && p.attempts[priority] > 0 ||
			!deadline.IsZero() && !now.Before(deadline)) {
			p.skipped[priority] = true
			continue
		}
		if remaining <= 1 || len(tier) == 1 && !hasLower {
			return tier, time.Time{}
		}
		// One half of the current remaining time is held for another attempt.
		// The parent's deadline still wins, including caller cancellation.
		callDeadline := time.Time{}
		if !p.deadline.IsZero() {
			callDeadline = now.Add(p.deadline.Sub(now) / 2)
		}
		if hasLower {
			if deadline.IsZero() {
				deadline = now.Add(settings.priorityTimeout())
				if !callDeadline.IsZero() && callDeadline.Before(deadline) {
					deadline = callDeadline
				}
				p.deadlines[priority] = deadline
			}
			if callDeadline.IsZero() || deadline.Before(callDeadline) {
				callDeadline = deadline
			}
		}
		return tier, callDeadline
	}
}
