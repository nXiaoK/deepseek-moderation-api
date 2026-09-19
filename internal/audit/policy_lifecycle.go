package audit

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

func policyChangeSummary(before, after PolicySettings) map[string]any {
	oldRaw, _ := json.Marshal(before)
	newRaw, _ := json.Marshal(after)
	var oldFields, newFields map[string]json.RawMessage
	_ = json.Unmarshal(oldRaw, &oldFields)
	_ = json.Unmarshal(newRaw, &newFields)
	changes := map[string]any{}
	for key, value := range newFields {
		if bytes.Equal(oldFields[key], value) {
			continue
		}
		if key == "prompt" {
			changes[key] = map[string]any{"before_sha256": digest(before.Prompt), "after_sha256": digest(after.Prompt), "characters": utf8.RuneCountInString(after.Prompt)}
		} else {
			changes[key] = value
		}
	}
	// omitempty fields disappear from the new JSON when cleared. Record those
	// removals explicitly instead of reporting an empty change set.
	for key := range oldFields {
		if _, exists := newFields[key]; !exists {
			changes[key] = nil
		}
	}
	return changes
}
func (s *Store) ArchivePolicy(ctx context.Context, actor, id string, revision int64, archived bool) error {
	return s.mutate(ctx, actor, "policy.archive", id, func(tx *sql.Tx) error {
		p, err := scanPolicy(tx.QueryRowContext(ctx, "SELECT "+policyColumns+" FROM audit_policies WHERE id=$1 FOR UPDATE", id))
		if err != nil {
			return err
		}
		if p.Revision != revision {
			return ErrConflict
		}
		if p.Archived == archived {
			return problem(409, "policy_state_unchanged", "策略已经处于该归档状态")
		}
		var running bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM evaluation_runs WHERE policy_id=$1 AND status IN ('queued','running'))", id).Scan(&running); err != nil {
			return err
		}
		if running {
			return problem(409, "policy_in_evaluation", "策略正在评测中，请先完成或取消评测")
		}
		_, err = tx.ExecContext(ctx, "UPDATE audit_policies SET archived=$2,enabled=FALSE,revision=revision+1,updated_at=NOW() WHERE id=$1", id, archived)
		return err
	}, map[string]bool{"archived": archived, "enabled": false})
}
func (s *Server) archivePolicy(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Archived *bool `json:"archived"`
		Revision int64 `json:"expected_revision"`
	}
	if err := readJSON(w, r, &in); err != nil {
		return err
	}
	if in.Archived == nil {
		return problem(400, "invalid_state", "archived 必须为布尔值")
	}
	if err := s.Store.ArchivePolicy(r.Context(), actor(r), r.PathValue("id"), in.Revision, *in.Archived); err != nil {
		return err
	}
	return s.getPolicy(w, r)
}
func (s *Store) DeletePolicy(ctx context.Context, actor, id string) error {
	return s.mutate(ctx, actor, "policy.delete", id, func(tx *sql.Tx) error {
		p, err := scanPolicy(tx.QueryRowContext(ctx, "SELECT "+policyColumns+" FROM audit_policies WHERE id=$1 FOR UPDATE", id))
		if err != nil {
			return err
		}
		if !p.Archived {
			return problem(409, "policy_not_archived", "请先归档策略")
		}
		var used bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM client_api_keys WHERE deleted_at IS NULL AND policy_ids ? $1) OR EXISTS(SELECT 1 FROM evaluation_runs WHERE policy_id=$1 AND status IN ('queued','running'))`, id).Scan(&used); err != nil {
			return err
		}
		if used {
			return problem(409, "policy_in_use", "策略仍被访问密钥或运行中的评测引用，请先解除授权或结束评测")
		}
		_, err = tx.ExecContext(ctx, "DELETE FROM audit_policies WHERE id=$1", id)
		return err
	})
}
func (s *Server) deletePolicy(w http.ResponseWriter, r *http.Request) error {
	if err := s.Store.DeletePolicy(r.Context(), actor(r), r.PathValue("id")); err != nil {
		return err
	}
	return writeJSON(w, 200, map[string]bool{"ok": true})
}

type policyFile struct {
	SchemaVersion int            `json:"schema_version"`
	Name          string         `json:"name"`
	Alias         string         `json:"alias"`
	Config        PolicySettings `json:"config"`
}

func (s *Server) exportPolicy(w http.ResponseWriter, r *http.Request) error {
	p, err := s.Store.Policy(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	w.Header().Set("Content-Disposition", `attachment; filename="policy-`+p.ID+`.json"`)
	return writeJSON(w, 200, policyFile{1, p.Name, p.Alias, p.Config})
}
func (s *Server) importPolicy(w http.ResponseWriter, r *http.Request) error {
	var in policyFile
	if err := readJSON(w, r, &in); err != nil {
		return err
	}
	if in.SchemaVersion != 1 || strings.TrimSpace(in.Name) == "" || len(in.Name) > 200 || !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,79}$`).MatchString(in.Alias) {
		return problem(400, "invalid_policy", "配置版本、名称或模型别名无效")
	}
	if in.Config.StoreModelOutput == nil {
		enabled := false
		in.Config.StoreModelOutput = &enabled
	}
	if in.Config.ModelOutputRetentionDays == 0 {
		in.Config.ModelOutputRetentionDays = 7
	}
	if err := in.Config.Validate(); err != nil {
		return problem(400, "invalid_config", err.Error())
	}
	p := Policy{ID: randomToken("pol_"), Name: in.Name, Alias: in.Alias, Config: in.Config, Revision: 1, UpdatedAt: time.Now().UTC()}
	raw, _ := json.Marshal(p.Config)
	err := s.Store.mutate(r.Context(), actor(r), "policy.import", p.ID, func(tx *sql.Tx) error {
		if err := validateBindings(r.Context(), tx, p.Config, false); err != nil {
			return err
		}
		res, err := tx.ExecContext(r.Context(), "INSERT INTO audit_policies(id,name,alias,config) VALUES($1,$2,$3,$4) ON CONFLICT(alias) DO NOTHING", p.ID, p.Name, p.Alias, string(raw))
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return problem(409, "alias_exists", "模型别名已存在")
		}
		return nil
	}, map[string]string{"name": p.Name, "alias": p.Alias, "prompt_sha256": digest(p.Config.Prompt)})
	if err != nil {
		return err
	}
	return writeJSON(w, 201, p)
}
