package audit

import (
	"context"
	"database/sql"
	"net/http"
)

// Keep the client identity for historical costs, budgets and in-flight
// settlement, while irreversibly removing the token's authentication verifier.
func (s *Store) DeleteKey(ctx context.Context, actor, id string) error {
	return s.mutate(ctx, actor, "key.delete", id, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE client_api_keys SET active=FALSE,deleted_at=NOW(),token_hash=$2 WHERE id=$1 AND deleted_at IS NULL`, id, digest(randomToken("deleted_")))
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrNotFound
		}
		return nil
	})
}

func (s *Server) deleteKey(w http.ResponseWriter, r *http.Request) error {
	if err := s.Store.DeleteKey(r.Context(), actor(r), r.PathValue("id")); err != nil {
		return err
	}
	return writeJSON(w, 200, map[string]bool{"ok": true})
}
