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

const channelColumns = `c.id,c.name,c.model,c.credential_id,c.timeout_ms,c.max_tokens,c.max_concurrency,c.enabled,c.revision,c.cache_epoch,k.provider,k.base_url,k.active,c.text_only`

func scanChannel(row scanner) (ModelChannel, error) {
	var c ModelChannel
	err := row.Scan(&c.ID, &c.Name, &c.Model, &c.CredentialID, &c.TimeoutMS, &c.MaxTokens, &c.MaxConcurrency, &c.Enabled, &c.Revision, &c.CacheEpoch, &c.Provider, &c.BaseURL, &c.CredentialActive, &c.TextOnly)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}
func validateBindings(ctx context.Context, tx *sql.Tx, cfg PolicySettings, required bool) error {
	if err := cfg.Validate(); err != nil {
		return problem(400, "invalid_config", err.Error())
	}
	raw, _ := json.Marshal(cfg.Channels)
	rows, err := tx.QueryContext(ctx, `SELECT `+channelColumns+` FROM audit_model_channels c JOIN provider_credentials k ON k.id=c.credential_id WHERE c.id IN (SELECT value->>'channel_id' FROM jsonb_array_elements($1::jsonb)) ORDER BY c.id FOR SHARE OF c,k`, string(raw))
	if err != nil {
		return err
	}
	defer rows.Close()
	found := map[string]ModelChannel{}
	for rows.Next() {
		c, e := scanChannel(rows)
		if e != nil {
			return e
		}
		found[c.ID] = c
	}
	if err = rows.Err(); err != nil {
		return err
	}
	ready := false
	for _, b := range cfg.Channels {
		c, ok := found[b.ChannelID]
		if !ok {
			return problem(400, "invalid_channel", "所选模型通道不存在")
		}
		if b.Enabled && c.Enabled && c.CredentialActive {
			ready = true
		}
	}
	if required && !ready {
		return problem(400, "channel_required", "启用策略至少需要一个已启用且密钥有效的模型通道")
	}
	return nil
}

// One repeatable-read snapshot prevents mixing policy and channel edits.
func (s *Store) RouteSnapshot(ctx context.Context, id string, preview *PolicySettings) (Policy, []ModelChannel, error) {
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return Policy{}, nil, err
	}
	defer tx.Rollback()
	p, err := scanPolicy(tx.QueryRowContext(ctx, "SELECT "+policyColumns+" FROM audit_policies WHERE id=$1", id))
	if err != nil {
		return p, nil, err
	}
	if p.Archived {
		return p, nil, problem(409, "policy_archived", "策略已归档，请先恢复")
	}
	if preview != nil {
		p.Config = *preview
	}
	if err = p.Config.Validate(); err != nil {
		return p, nil, problem(400, "invalid_config", err.Error())
	}
	raw, _ := json.Marshal(p.Config.Channels)
	rows, err := tx.QueryContext(ctx, `SELECT `+channelColumns+` FROM audit_model_channels c JOIN provider_credentials k ON k.id=c.credential_id WHERE c.id IN (SELECT value->>'channel_id' FROM jsonb_array_elements($1::jsonb)) ORDER BY c.id`, string(raw))
	if err != nil {
		return p, nil, err
	}
	channels := []ModelChannel{}
	for rows.Next() {
		c, e := scanChannel(rows)
		if e != nil {
			rows.Close()
			return p, nil, e
		}
		channels = append(channels, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return p, nil, err
	}
	if len(channels) != len(p.Config.Channels) {
		return p, nil, problem(400, "invalid_channel", "所选模型通道不存在")
	}
	return p, channels, tx.Commit()
}
func (s *Store) Channels(ctx context.Context) ([]ModelChannel, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT "+channelColumns+" FROM audit_model_channels c JOIN provider_credentials k ON k.id=c.credential_id ORDER BY c.name,c.id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []ModelChannel{}
	for rows.Next() {
		c, e := scanChannel(rows)
		if e != nil {
			return nil, e
		}
		items = append(items, c)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	policies, err := s.PoliciesIncludingArchived(ctx, true)
	if err != nil {
		return nil, err
	}
	for i := range items {
		items[i].PolicyNames = []string{}
		for _, p := range policies {
			for _, b := range p.Config.Channels {
				if b.ChannelID == items[i].ID {
					name := p.Name
					if p.Archived {
						name += "（已归档）"
					}
					items[i].PolicyNames = append(items[i].PolicyNames, name)
				}
			}
		}
	}
	return items, nil
}
func (s *Store) SaveChannel(ctx context.Context, actor string, c ModelChannel) (ModelChannel, error) {
	if strings.TrimSpace(c.Name) == "" || len(c.Name) > 200 || c.MaxConcurrency < 1 || c.MaxConcurrency > 256 {
		return c, problem(400, "invalid_channel", "请输入通道名称，并发上限必须为 1～256")
	}
	create := c.ID == ""
	if create {
		c.ID = randomToken("chan_")
	}
	err := s.mutate(ctx, actor, "channel.save", c.ID, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, "SELECT provider,base_url,active FROM provider_credentials WHERE id=$1 FOR SHARE", c.CredentialID).Scan(&c.Provider, &c.BaseURL, &c.CredentialActive)
		if errors.Is(err, sql.ErrNoRows) {
			return problem(400, "credential_required", "请选择有效的连接密钥")
		}
		if err != nil {
			return err
		}
		if c.Enabled && !c.CredentialActive {
			return problem(400, "credential_required", "通道启用前请先启用连接密钥")
		}
		if err = c.Inference(DefaultSettings()).Validate(); err != nil {
			return problem(400, "invalid_channel", err.Error())
		}
		if create {
			_, err = tx.ExecContext(ctx, `INSERT INTO audit_model_channels(id,name,model,credential_id,timeout_ms,max_tokens,max_concurrency,enabled,cache_epoch,text_only) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, c.ID, c.Name, c.Model, c.CredentialID, c.TimeoutMS, c.MaxTokens, c.MaxConcurrency, c.Enabled, randomToken("cache_"), c.TextOnly)
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE audit_model_channels SET name=$1,model=$2,credential_id=$3,timeout_ms=$4,max_tokens=$5,max_concurrency=$6,enabled=$7,revision=revision+1,cache_epoch=$8,text_only=$11 WHERE id=$9 AND revision=$10`, c.Name, c.Model, c.CredentialID, c.TimeoutMS, c.MaxTokens, c.MaxConcurrency, c.Enabled, randomToken("cache_"), c.ID, c.Revision, c.TextOnly)
		if err != nil {
			return err
		}
		n, _ := result.RowsAffected()
		if n != 1 {
			return ErrConflict
		}
		return nil
	})
	if err != nil {
		return c, err
	}
	return scanChannel(s.DB.QueryRowContext(ctx, "SELECT "+channelColumns+" FROM audit_model_channels c JOIN provider_credentials k ON k.id=c.credential_id WHERE c.id=$1", c.ID))
}
func (s *Server) channels(w http.ResponseWriter, r *http.Request) error {
	items, err := s.Store.Channels(r.Context())
	if err != nil {
		return err
	}
	for i := range items {
		items[i].Health = s.currentChannelHealth(items[i])
	}
	return writeJSON(w, 200, items)
}
func (s *Server) currentChannelHealth(c ModelChannel) ChannelHealth {
	health := s.Engine.channelHealth(c.ID)
	if health.ConfigurationRevision != c.Revision {
		health.Status = "ready"
		health.Verified = false
		health.LastSuccessAt = nil
		health.LastFailureAt = nil
		health.LastErrorCode = ""
		health.CooldownUntil = time.Time{}
	}
	if err := c.Inference(DefaultSettings()).Validate(); err != nil {
		health.Status = "configuration_error"
		health.Verified = false
		health.LastErrorCode = "invalid_config"
	}
	return health
}
func (s *Server) saveChannel(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Name           string `json:"name"`
		Model          string `json:"model"`
		CredentialID   string `json:"credential_id"`
		TimeoutMS      int    `json:"timeout_ms"`
		MaxTokens      int    `json:"max_tokens"`
		MaxConcurrency int    `json:"max_concurrency"`
		TextOnly       bool   `json:"text_only"`
		Enabled        bool   `json:"enabled"`
		Revision       int64  `json:"expected_revision"`
	}
	if err := readJSON(w, r, &in); err != nil {
		return err
	}
	c, err := s.Store.SaveChannel(r.Context(), actor(r), ModelChannel{ID: r.PathValue("id"), Name: in.Name, Model: in.Model, CredentialID: in.CredentialID, TimeoutMS: in.TimeoutMS, MaxTokens: in.MaxTokens, MaxConcurrency: in.MaxConcurrency, TextOnly: in.TextOnly, Enabled: in.Enabled, Revision: in.Revision})
	if err != nil {
		return err
	}
	s.Engine.resetChannel(c.ID)
	return writeJSON(w, 200, c)
}
func (s *Server) deleteChannel(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	err := s.Store.mutate(r.Context(), actor(r), "channel.delete", id, func(tx *sql.Tx) error {
		// Policy writers share-lock this channel before saving references.
		var found string
		if err := tx.QueryRowContext(r.Context(), "SELECT id FROM audit_model_channels WHERE id=$1 FOR UPDATE", id).Scan(&found); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		var used bool
		if err := tx.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM audit_policies p,jsonb_array_elements(p.config->'channels') b WHERE b->>'channel_id'=$1)`, id).Scan(&used); err != nil {
			return err
		}
		if used {
			return problem(409, "channel_in_use", "通道仍被策略引用，请先解除绑定")
		}
		_, err := tx.ExecContext(r.Context(), "DELETE FROM audit_model_channels WHERE id=$1", id)
		return err
	})
	if err != nil {
		return err
	}
	return writeJSON(w, 200, map[string]bool{"ok": true})
}
func (s *Server) clearChannelCache(w http.ResponseWriter, r *http.Request) error {
	err := s.Store.mutate(r.Context(), actor(r), "channel.cache_clear", r.PathValue("id"), func(tx *sql.Tx) error {
		result, err := tx.ExecContext(r.Context(), "UPDATE audit_model_channels SET cache_epoch=$1,revision=revision+1 WHERE id=$2", randomToken("cache_"), r.PathValue("id"))
		if err != nil {
			return err
		}
		n, _ := result.RowsAffected()
		if n != 1 {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.Engine.resetChannel(r.PathValue("id"))
	return writeJSON(w, 200, map[string]bool{"ok": true})
}
