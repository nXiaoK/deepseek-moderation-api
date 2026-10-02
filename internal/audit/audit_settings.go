package audit

import (
	"context"
	"database/sql"
	"net/http"
)

// ReasonLimitCeiling is a storage safety bound, not the active model-output limit.
const ReasonLimitCeiling = 4096

type AuditSettings struct {
	ReasonMaxChars int   `json:"reason_max_chars"`
	Revision       int64 `json:"revision"`
}

func (s *Store) auditSettings(ctx context.Context) (AuditSettings, error) {
	var v AuditSettings
	err := s.DB.QueryRowContext(ctx, "SELECT reason_max_chars,revision FROM audit_settings WHERE id=TRUE").Scan(&v.ReasonMaxChars, &v.Revision)
	return v, err
}

func (s *Server) getAuditSettings(w http.ResponseWriter, r *http.Request) error {
	v, err := s.Store.auditSettings(r.Context())
	if err != nil {
		return err
	}
	return writeJSON(w, 200, v)
}

func (s *Server) saveAuditSettings(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		ReasonMaxChars   int   `json:"reason_max_chars"`
		ExpectedRevision int64 `json:"expected_revision"`
	}
	if err := readJSON(w, r, &in); err != nil {
		return err
	}
	if in.ReasonMaxChars < 1 || in.ReasonMaxChars > ReasonLimitCeiling {
		return problem(400, "invalid_audit_settings", "reason 字数上限必须为 1～4096 的整数")
	}
	err := s.Store.mutate(r.Context(), actor(r), "settings.audit", "audit", func(tx *sql.Tx) error {
		result, err := tx.ExecContext(r.Context(), "UPDATE audit_settings SET reason_max_chars=$1,revision=revision+1 WHERE id=TRUE AND revision=$2", in.ReasonMaxChars, in.ExpectedRevision)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count == 0 {
			return ErrConflict
		}
		return nil
	})
	if err != nil {
		return err
	}
	return s.getAuditSettings(w, r)
}

func (c PolicyConfig) reasonMaxChars() int {
	if c.ReasonMaxChars == 0 {
		return MaxReasonRunes
	}
	return c.ReasonMaxChars
}
