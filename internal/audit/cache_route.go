package audit

import (
	"context"
	"database/sql"
	"errors"
	"math/rand/v2"
	"slices"
	"strings"
	"time"
)

func cacheImages(c routeCandidate, images []AuditImage) []AuditImage {
	if c.channel.TextOnly {
		return nil
	}
	return images
}
func (s *Store) prepareRouteCacheKeys(client string, p Policy, candidates []routeCandidate, input string, images []AuditImage) {
	promptDigest := digest(p.Config.Prompt)
	textDigest := s.cacheInputDigest(input, nil)
	imageDigest := textDigest
	cacheableImages := imageInputCacheable(images)
	if len(images) > 0 && cacheableImages {
		imageDigest = s.cacheInputDigest(input, images)
	}
	for i := range candidates {
		c := &candidates[i]
		if !c.channel.TextOnly && !cacheableImages {
			continue
		}
		inputDigest := imageDigest
		if c.channel.TextOnly {
			inputDigest = textDigest
		}
		c.cacheKey = s.assessmentCacheKeyFromInput(client, p.ID, c.cfg, int(p.Revision), c.key, inputDigest, promptDigest)
	}
}
func (s *Server) cacheLock(ctx context.Context, candidates []routeCandidate) (func(), error) {
	keys := make([]string, 0, len(candidates))
	for _, c := range candidates {
		if c.cacheKey == "" {
			return func() {}, nil
		}
		keys = append(keys, c.cacheKey)
	}
	if len(keys) == 0 {
		return func() {}, nil
	}
	slices.Sort(keys)
	return s.Engine.cacheGate.acquire(ctx, digest(strings.Join(keys, ":")))
}

// Cache selection observes configured priority/weight, but does not consume
// upstream capacity or change the channel's circuit-breaker state.
func (s *Server) cachedRoute(ctx context.Context, p Policy, candidates []routeCandidate, client, input string, response *Response, log *AuditLog, images []AuditImage) (Assessment, bool, error) {
	keys := make([]string, 0, len(candidates))
	for _, c := range candidates {
		if c.cacheKey != "" {
			keys = append(keys, c.cacheKey)
		}
	}
	values, err := s.Store.cachedAssessments(ctx, keys)
	if err != nil {
		return Assessment{}, false, problem(503, "cache_unavailable", "审核缓存暂时不可用")
	}
	remaining := slices.Clone(candidates)
	for len(remaining) > 0 {
		priority, weight := 101, 0
		for _, c := range remaining {
			if c.binding.Priority < priority {
				priority = c.binding.Priority
				weight = 0
			}
			if c.binding.Priority == priority {
				weight += c.binding.Weight
			}
		}
		pick, index := rand.IntN(weight), 0
		for i, c := range remaining {
			if c.binding.Priority != priority {
				continue
			}
			pick -= c.binding.Weight
			if pick < 0 {
				index = i
				break
			}
		}
		c := remaining[index]
		remaining = append(remaining[:index], remaining[index+1:]...)
		value := values[c.cacheKey]
		if value == nil {
			continue
		}
		var active bool
		err = s.Store.DB.QueryRowContext(ctx, `SELECT c.enabled AND k.active AND p.enabled AND NOT p.archived FROM audit_model_channels c JOIN provider_credentials k ON k.id=c.credential_id JOIN audit_policies p ON p.id=$2 WHERE c.id=$1 AND c.revision=$3 AND c.credential_id=$4`, c.channel.ID, p.ID, c.channel.Revision, c.channel.CredentialID).Scan(&active)
		if errors.Is(err, sql.ErrNoRows) || err == nil && !active {
			continue
		}
		if err != nil {
			return Assessment{}, false, err
		}
		at := time.Now()
		id := randomToken("cost_")
		entry, err := s.Store.ReserveCost(ctx, id, client, "production", p, c.cfg, input, true, at, response.ID, c.channel.ID)
		if err != nil {
			return Assessment{}, false, err
		}
		actual := value.ActualModel
		if actual == "" {
			actual = c.channel.Model
		}
		usage := Usage{Reported: true, ActualModel: actual}
		settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		cost, err := s.Store.SettleCost(settleCtx, entry, usage, time.Now(), nil)
		cancel()
		if err != nil {
			s.notePersistenceFailure(response.ID, "cached_cost")
			return Assessment{}, false, problem(503, "cost_record_unavailable", "缓存费用记录暂时不可用")
		}
		scope := auditInputScope(c.channel.TextOnly, images)
		log.Attempts = append(log.Attempts, AuditAttempt{ID: id, ChannelID: c.channel.ID, ChannelName: c.channel.Name, Provider: c.channel.Provider, Model: c.channel.Model, CacheHit: true, Usage: usage, Cost: cost, InputScope: scope, ImageCount: len(cacheImages(c, images)), LatencyMS: time.Since(at).Milliseconds()})
		response.ChannelID = c.channel.ID
		response.Provider = c.channel.Provider
		response.ActualModel = actual
		response.InputScope = scope
		response.CacheHit = true
		response.Usage = usage
		return value.Assessment, true, nil
	}
	return Assessment{}, false, nil
}
