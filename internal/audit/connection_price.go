package audit

import (
	"database/sql"
	"errors"
	"net/http"
	"time"
)

func (s *Server) resetConnectionPrice(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	err := s.Store.mutate(r.Context(), actor(r), "price.reset", id, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(r.Context(), "SELECT pg_advisory_xact_lock(846274903)"); err != nil {
			return err
		}
		var model, credential string
		var current string
		if err := tx.QueryRowContext(r.Context(), "SELECT model,credential_id FROM model_prices WHERE id::text=$1", id).Scan(&model, &credential); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if credential == "" {
			return problem(400, "default_price_required", "模型默认价格不能取消")
		}
		var active bool
		if err := tx.QueryRowContext(r.Context(), "SELECT id::text,active FROM model_prices WHERE model=$1 AND credential_id=$2 AND effective_at<=$3 ORDER BY effective_at DESC,id DESC LIMIT 1", model, credential, time.Now()).Scan(&current, &active); err != nil {
			return err
		}
		if current != id || !active {
			return ErrConflict
		}
		_, err := tx.ExecContext(r.Context(), `INSERT INTO model_prices(model,rates,source,author,credential_id,active,effective_at) SELECT model,rates,'恢复模型默认价格',$2,credential_id,FALSE,$3 FROM model_prices WHERE id::text=$1`, id, actor(r), time.Now())
		return err
	})
	if err != nil {
		return err
	}
	return s.prices(w, r)
}
