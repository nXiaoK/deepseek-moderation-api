package audit

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
)

func (s *Store) DeleteCredential(ctx context.Context, actor, id string) error {
	return s.mutate(ctx, actor, "credential.delete", id, func(tx *sql.Tx) error {
		var found string
		if err := tx.QueryRowContext(ctx, "SELECT id FROM provider_credentials WHERE id=$1 FOR UPDATE", id).Scan(&found); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		var used bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM audit_model_channels WHERE credential_id=$1)", id).Scan(&used); err != nil {
			return err
		}
		if used {
			return problem(409, "credential_in_use", "连接密钥仍被模型通道引用，请先在“审核模型”更换连接密钥或删除引用通道（包括已停用通道）")
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM provider_credentials WHERE id=$1", id)
		return err
	})
}

func (s *Server) deleteCredential(w http.ResponseWriter, r *http.Request) error {
	if err := s.Store.DeleteCredential(r.Context(), actor(r), r.PathValue("id")); err != nil {
		return err
	}
	return writeJSON(w, 200, map[string]bool{"ok": true})
}
