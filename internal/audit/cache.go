package audit

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

func (s *Store) assessmentCacheKey(client string, p Policy, cfg PolicyConfig, version int, credential, text string, images ...AuditImage) string {
	raw, _ := json.Marshal(struct {
		Client, Policy   string
		Version          int
		Config           PolicyConfig
		Credential, Text string
		Images           []AuditImage `json:",omitempty"`
	}{client, p.ID, version, cfg, digest(credential), text, images})
	h := hmac.New(sha256.New, s.Vault.hashKey)
	_, _ = h.Write([]byte("audit-result-cache-v1\x00"))
	_, _ = h.Write(raw)
	return hex.EncodeToString(h.Sum(nil))
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
