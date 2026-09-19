package audit

import (
	"log/slog"
	"sync"
	"time"
)

// The guard performs no I/O. Its client map has a hard cardinality bound; when
// full, new addresses wait for idle entries to expire instead of evicting live
// limits. Tokens bound both sustained traffic and a one-minute initial burst.
type ingressLimits struct {
	globalRPM, ipRPM, concurrency, ipConcurrency, maxClients int
}

type ingressBucket struct {
	tokens  float64
	updated time.Time
	active  int
}

type ingressGuard struct {
	mu           sync.Mutex
	limits       ingressLimits
	global       ingressBucket
	clients      map[string]*ingressBucket
	rejected     uint64
	lastReported time.Time
	nextCleanup  time.Time
}

func refillIngress(b *ingressBucket, rpm int, now time.Time) {
	if b.updated.IsZero() {
		b.tokens = float64(rpm)
	} else if elapsed := now.Sub(b.updated).Seconds(); elapsed > 0 {
		b.tokens = min(float64(rpm), b.tokens+elapsed*float64(rpm)/60)
	} else {
		return
	}
	b.updated = now
}

func (g *ingressGuard) acquire(ip string, now time.Time) (func(), error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.clients == nil {
		g.clients = make(map[string]*ingressBucket)
	}
	b := g.clients[ip]
	if b == nil && len(g.clients) >= g.limits.maxClients {
		if !now.Before(g.nextCleanup) {
			for key, candidate := range g.clients {
				if candidate.active == 0 && now.Sub(candidate.updated) >= time.Minute {
					delete(g.clients, key)
				}
			}
			g.nextCleanup = now.Add(time.Minute)
		}
		if len(g.clients) >= g.limits.maxClients {
			return nil, problem(503, "ingress_capacity_exceeded", "入口容量已满，请稍后重试")
		}
	}
	if b == nil {
		b = &ingressBucket{}
	}
	refillIngress(b, g.limits.ipRPM, now)
	refillIngress(&g.global, g.limits.globalRPM, now)
	// A blocked source must not consume the global budget or database work.
	if b.tokens < 1 || g.global.tokens < 1 {
		return nil, problem(429, "ingress_rate_limited", "入口请求过多，请稍后重试")
	}
	if b.active >= g.limits.ipConcurrency || g.global.active >= g.limits.concurrency {
		return nil, problem(503, "request_capacity_exceeded", "在途请求已满，请稍后重试")
	}
	g.clients[ip] = b
	b.tokens--
	g.global.tokens--
	b.active++
	g.global.active++
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			b.active--
			g.global.active--
			g.mu.Unlock()
		})
	}, nil
}

// Rejections never create audit rows. Emit at most one aggregate service log
// per 30 seconds, with a cumulative count and no client-controlled strings.
func (g *ingressGuard) recordRejection(now time.Time) {
	g.mu.Lock()
	g.rejected++
	count := g.rejected
	report := g.lastReported.IsZero() || now.Sub(g.lastReported) >= 30*time.Second
	if report {
		g.lastReported = now
	}
	g.mu.Unlock()
	if report {
		slog.Warn("moderation ingress requests rejected", "rejected_total", count)
	}
}
