package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

func keyExpiry(raw *string, allowPast bool) (*time.Time, error) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return nil, nil
	}
	at, err := time.Parse(time.RFC3339, *raw)
	if err != nil || !allowPast && !at.After(time.Now()) {
		return nil, problem(400, "invalid_expiry", "到期时间必须包含时区；新密钥到期时间须晚于当前时间")
	}
	return &at, nil
}
func (s *Store) UpdateKey(ctx context.Context, actor string, key ClientKey, revision int64) error {
	if strings.TrimSpace(key.Name) == "" || len(key.Name) > 200 || len(key.PolicyIDs) > 100 || key.Active && len(key.PolicyIDs) == 0 || key.RPM < 1 || key.RPM > 10000 {
		return problem(400, "invalid_key_config", "请输入名称、策略授权和每分钟限额（1～10000）；启用密钥至少需要一个策略")
	}
	if key.PolicyIDs == nil {
		key.PolicyIDs = []string{}
	}
	return s.mutate(ctx, actor, "key.update", key.ID, func(tx *sql.Tx) error {
		var current int64
		if err := tx.QueryRowContext(ctx, "SELECT revision FROM client_api_keys WHERE id=$1 AND deleted_at IS NULL FOR UPDATE", key.ID).Scan(&current); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if current != revision {
			return ErrConflict
		}
		seen := map[string]bool{}
		for _, id := range key.PolicyIDs {
			if seen[id] {
				return problem(400, "invalid_policy", "策略不能重复")
			}
			seen[id] = true
			var found bool
			if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM audit_policies WHERE id=$1)", id).Scan(&found); err != nil {
				return err
			}
			if !found {
				return problem(400, "invalid_policy", "所选策略不存在")
			}
		}
		raw, _ := json.Marshal(key.PolicyIDs)
		_, err := tx.ExecContext(ctx, "UPDATE client_api_keys SET name=$1,policy_ids=$2,rpm=$3,active=$4,expires_at=$5,revision=revision+1 WHERE id=$6", key.Name, string(raw), key.RPM, key.Active, key.ExpiresAt, key.ID)
		return err
	})
}
func (s *Server) updateKey(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Name      string   `json:"name"`
		PolicyIDs []string `json:"policy_ids"`
		RPM       int      `json:"rpm"`
		Active    *bool    `json:"active"`
		ExpiresAt *string  `json:"expires_at"`
		Revision  int64    `json:"expected_revision"`
	}
	if err := readJSON(w, r, &in); err != nil {
		return err
	}
	expiry, err := keyExpiry(in.ExpiresAt, true)
	if err != nil {
		return err
	}
	if in.Active == nil {
		return problem(400, "invalid_key_config", "active 必须为布尔值")
	}
	if err := s.Store.UpdateKey(r.Context(), actor(r), ClientKey{ID: r.PathValue("id"), Name: in.Name, PolicyIDs: in.PolicyIDs, RPM: in.RPM, Active: *in.Active, ExpiresAt: expiry}, in.Revision); err != nil {
		return err
	}
	return writeJSON(w, 200, map[string]bool{"ok": true})
}
func (s *Store) RotateKey(ctx context.Context, actor, id string, revision int64) (string, error) {
	token := randomToken("dsa_")
	err := s.mutate(ctx, actor, "key.rotate", id, func(tx *sql.Tx) error {
		var current int64
		if err := tx.QueryRowContext(ctx, "SELECT revision FROM client_api_keys WHERE id=$1 AND deleted_at IS NULL FOR UPDATE", id).Scan(&current); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if current != revision {
			return ErrConflict
		}
		_, err := tx.ExecContext(ctx, "UPDATE client_api_keys SET token_hash=$1,prefix=$2,rotated_at=NOW(),revision=revision+1 WHERE id=$3", digest(token), token[:12], id)
		return err
	})
	return token, err
}
func (s *Server) rotateKey(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Revision int64 `json:"expected_revision"`
	}
	if err := readJSON(w, r, &in); err != nil {
		return err
	}
	token, err := s.Store.RotateKey(r.Context(), actor(r), r.PathValue("id"), in.Revision)
	if err != nil {
		return err
	}
	return writeJSON(w, 200, map[string]string{"token": token})
}
