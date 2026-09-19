package audit

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"time"

	"github.com/lib/pq"
)

func (s *Store) assessmentCacheKey(client string, p Policy, cfg PolicyConfig, version int, credential, text string, images ...AuditImage) string {
	return s.assessmentCacheKeyFromInput(client, p.ID, cfg, version, credential, s.cacheInputDigest(text, images), digest(cfg.Prompt))
}

// Length framing avoids ambiguous concatenations. Bounded chunks prevent a
// large data URL from allocating another equally large temporary byte slice.
func writeCacheString(h hash.Hash, value string) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = h.Write(size[:])
	for len(value) > 0 {
		n := min(len(value), 32*1024)
		_, _ = h.Write([]byte(value[:n]))
		value = value[n:]
	}
}

func (s *Store) cacheInputDigest(text string, images []AuditImage) string {
	h := hmac.New(sha256.New, s.Vault.hashKey)
	writeCacheString(h, "audit-result-input-v2")
	writeCacheString(h, text)
	for _, image := range images {
		writeCacheString(h, image.URL)
		writeCacheString(h, image.Detail)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (s *Store) assessmentCacheKeyFromInput(client, policy string, cfg PolicyConfig, version int, credential, inputDigest, promptDigest string) string {
	cfg.Prompt = promptDigest
	raw, _ := json.Marshal(struct {
		Client, Policy          string
		Version                 int
		Config                  PolicyConfig
		Credential, InputDigest string
	}{client, policy, version, cfg, digest(credential), inputDigest})
	h := hmac.New(sha256.New, s.Vault.hashKey)
	_, _ = h.Write([]byte("audit-result-cache-v2\x00"))
	_, _ = h.Write(raw)
	return hex.EncodeToString(h.Sum(nil))
}

func (s *Store) cachedAssessments(ctx context.Context, keys []string) (map[string]*CachedResult, error) {
	items := make(map[string]*CachedResult, len(keys))
	if len(keys) == 0 {
		return items, nil
	}
	rows, err := s.DB.QueryContext(ctx, "SELECT cache_key,assessment,actual_model FROM assessment_cache WHERE cache_key=ANY($1) AND expires_at>NOW()", pq.Array(keys))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key, model string
		var raw []byte
		if err := rows.Scan(&key, &raw, &model); err != nil {
			return nil, err
		}
		value, err := ParseAssessment(raw)
		if err != nil {
			return nil, err
		}
		items[key] = &CachedResult{value, model}
	}
	return items, rows.Err()
}

type CachedResult struct {
	Assessment  Assessment
	ActualModel string
}

func (s *Store) CachedAssessment(ctx context.Context, key string) (*CachedResult, error) {
	var actualModel string
	var raw []byte
	err := s.DB.QueryRowContext(ctx, "SELECT assessment,actual_model FROM assessment_cache WHERE cache_key=$1 AND expires_at>NOW()", key).Scan(&raw, &actualModel)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	value, err := ParseAssessment(raw)
	if err != nil {
		return nil, err
	}
	return &CachedResult{value, actualModel}, nil
}
func (s *Store) CacheAssessment(ctx context.Context, key string, value Assessment, ttl int, actualModel string) error {
	value.Reason = redactReason(value.Reason)
	raw, _ := json.Marshal(value)
	_, err := s.DB.ExecContext(ctx, `INSERT INTO assessment_cache(cache_key,assessment,expires_at,actual_model) VALUES($1,$2,$3,$4) ON CONFLICT(cache_key) DO UPDATE SET assessment=$2,expires_at=$3,actual_model=$4`, key, string(raw), time.Now().Add(time.Duration(ttl)*time.Second), actualModel)
	return err
}
