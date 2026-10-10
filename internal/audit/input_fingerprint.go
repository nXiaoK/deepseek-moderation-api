package audit

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

var inputFingerprintPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func (s *Store) inputFingerprint(text string) string {
	h := hmac.New(sha256.New, s.Vault.hashKey)
	writeCacheString(h, "audit-input-ranking-v1")
	writeCacheString(h, text)
	return hex.EncodeToString(h.Sum(nil))
}

// Older failures qualify only when their metadata confirms input validation.
const legacyRankingEligibleSQL = `(metadata->'request'->>'stage' IN ('audit','completed','recording')
 AND COALESCE((metadata->'request'->>'text_chars')::integer,0)>0
 OR metadata->'request' IS NULL AND error_code='' AND jsonb_typeof(metadata->'confidence')='number')`

func (s *Store) backfillInputFingerprints(ctx context.Context, limit int) (int, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id,input_cipher FROM audit_requests
 WHERE kind='production' AND expires_at>NOW() AND NOT input_fingerprint_checked
 AND octet_length(input_cipher)>0 AND `+legacyRankingEligibleSQL+`
 ORDER BY created_at,id LIMIT $1 FOR UPDATE SKIP LOCKED`, limit)
	if err != nil {
		return 0, err
	}
	type candidate struct {
		id     string
		cipher []byte
	}
	items := []candidate{}
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.id, &item.cipher); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	for _, item := range items {
		text, err := s.Vault.Open(item.cipher, "input:"+item.id)
		if err != nil {
			slog.Warn("input fingerprint backfill skipped unreadable input", "request_id", item.id)
		}
		fingerprint := ""
		if err == nil && strings.TrimSpace(text) != "" && utf8.ValidString(text) && utf8.RuneCountInString(text) <= 64000 {
			fingerprint = s.inputFingerprint(text)
		}
		if _, err := tx.ExecContext(ctx, "UPDATE audit_requests SET input_fingerprint=NULLIF($2,''),input_fingerprint_checked=TRUE WHERE id=$1", item.id, fingerprint); err != nil {
			return 0, err
		}
	}
	return len(items), tx.Commit()
}

func (s *Server) StartInputFingerprintWorker() {
	s.evaluationMu.Lock()
	defer s.evaluationMu.Unlock()
	if s.closing || s.inputWorkerStarted {
		return
	}
	s.inputWorkerStarted = true
	s.inputWorkers.Add(1)
	go func() {
		defer s.inputWorkers.Done()
		for s.backgroundContext.Err() == nil {
			ctx, cancel := context.WithTimeout(s.backgroundContext, 5*time.Second)
			processed, err := s.Store.backfillInputFingerprints(ctx, 50)
			cancel()
			if err != nil && s.backgroundContext.Err() == nil {
				slog.Error("input fingerprint backfill failed")
			}
			delay := time.Minute
			if processed > 0 && err == nil {
				delay = 250 * time.Millisecond
			}
			timer := time.NewTimer(delay)
			select {
			case <-s.backgroundContext.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
}
