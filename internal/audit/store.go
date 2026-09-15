package audit

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/lib/pq"
)

//go:embed initial-prompt.txt
var InitialPrompt string

type Store struct {
	DB    *sql.DB
	Vault *Vault
}

func OpenStore(ctx context.Context, dsn string, vault *Vault) (*Store, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)
	if err = db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{db, vault}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		db.Close()
		return nil, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(846274901)"); err == nil {
		err = applyMigrations(ctx, tx)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}
func DefaultConfig() PolicyConfig {
	return PolicyConfig{Prompt: InitialPrompt, Threshold: .8, Model: "deepseek-flash", BaseURL: "https://api.deepseek.com", TimeoutMS: 7000, MaxTokens: 512, RetentionDays: 30}
}
func (s *Store) Bootstrap(ctx context.Context, username, password string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(846274902)"); err != nil {
		return err
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM admin_users").Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		if len(password) < 16 || len(password) > 256 || len(username) < 1 || len(username) > 80 {
			return errors.New("first startup requires ADMIN_USER and ADMIN_PASSWORD (16–256 characters)")
		}
		hash, err := hashPassword(password)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO admin_users VALUES($1,$2)", username, hash); err != nil {
			return err
		}
	}
	if count == 0 {
		raw, _ := json.Marshal(DefaultSettings())
		if _, err = tx.ExecContext(ctx, `INSERT INTO audit_policies(id,name,alias,config) VALUES('abuse-default','网络滥用与人身伤害审核','abuse-audit-v1',$1) ON CONFLICT DO NOTHING`, string(raw)); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *Store) mutate(ctx context.Context, actor, action, id string, fn func(*sql.Tx) error, details ...any) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Configuration writes are rare. Serialize them to keep reference/credential
	// locking order consistent across channel, policy and credential operations.
	if strings.HasPrefix(action, "policy.") || strings.HasPrefix(action, "channel.") || strings.HasPrefix(action, "credential.") || strings.HasPrefix(action, "key.") {
		if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(846274903)"); err != nil {
			return err
		}
	}
	if strings.HasPrefix(action, "evaluation.sample.") {
		if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(846274907)"); err != nil {
			return err
		}
	}
	if err = fn(tx); err != nil {
		return err
	}
	raw := []byte("{}")
	if len(details) > 0 {
		raw, err = json.Marshal(details[0])
		if err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO admin_action_logs(username,action,resource_id,details) VALUES($1,$2,$3,$4)", actor, action, id, string(raw)); err != nil {
		return err
	}
	return tx.Commit()
}

type scanner interface{ Scan(...any) error }

func scanPolicy(row scanner) (Policy, error) {
	var p Policy
	var raw []byte
	err := row.Scan(&p.ID, &p.Name, &p.Alias, &p.Enabled, &p.Revision, &raw, &p.UpdatedAt, &p.Archived)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	err = json.Unmarshal(raw, &p.Config)
	if p.Config.StoreModelOutput == nil {
		enabled := true
		p.Config.StoreModelOutput = &enabled
	}
	if p.Config.ModelOutputRetentionDays == 0 {
		p.Config.ModelOutputRetentionDays = p.Config.RetentionDays
	}
	return p, err
}

const policyColumns = "id,name,alias,enabled,revision,config,updated_at,archived"

func (s *Store) Policies(ctx context.Context) ([]Policy, error) {
	return s.PoliciesIncludingArchived(ctx, false)
}
func (s *Store) PoliciesIncludingArchived(ctx context.Context, include bool) ([]Policy, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT "+policyColumns+" FROM audit_policies WHERE ($1 OR NOT archived) ORDER BY name,id", include)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Policy{}
	for rows.Next() {
		p, err := scanPolicy(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, p)
	}
	return items, rows.Err()
}
func (s *Store) Policy(ctx context.Context, id string) (Policy, error) {
	return scanPolicy(s.DB.QueryRowContext(ctx, "SELECT "+policyColumns+" FROM audit_policies WHERE id=$1", id))
}
func (s *Store) PolicyByAlias(ctx context.Context, alias string) (Policy, error) {
	return scanPolicy(s.DB.QueryRowContext(ctx, "SELECT "+policyColumns+" FROM audit_policies WHERE alias=$1 AND NOT archived", alias))
}
func (s *Store) CreatePolicy(ctx context.Context, actor, name, alias, source string) (Policy, error) {
	cfg := DefaultSettings()
	if source != "" {
		p, err := s.Policy(ctx, source)
		if err != nil {
			return Policy{}, err
		}
		cfg = p.Config
	}
	id := randomToken("pol_")
	raw, _ := json.Marshal(cfg)
	err := s.mutate(ctx, actor, "policy.create", id, func(tx *sql.Tx) error {
		if err := validateBindings(ctx, tx, cfg, false); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, "INSERT INTO audit_policies(id,name,alias,config) VALUES($1,$2,$3,$4) ON CONFLICT(alias) DO NOTHING", id, name, alias, string(raw))
		if err != nil {
			return err
		}
		n, _ := result.RowsAffected()
		if n != 1 {
			return problem(409, "alias_exists", "模型别名已存在")
		}
		return nil
	})
	if err != nil {
		return Policy{}, err
	}
	return s.Policy(ctx, id)
}
func (s *Store) SaveConfig(ctx context.Context, actor, id, name string, revision int64, cfg PolicySettings) error {
	if err := cfg.Validate(); err != nil {
		return problem(400, "invalid_config", err.Error())
	}
	raw, _ := json.Marshal(cfg)
	details := map[string]any{}
	return s.mutate(ctx, actor, "policy.save", id, func(tx *sql.Tx) error {
		var enabled, archived bool
		var oldRaw []byte
		var oldName string
		if err := tx.QueryRowContext(ctx, "SELECT enabled,archived,config,name FROM audit_policies WHERE id=$1 FOR UPDATE", id).Scan(&enabled, &archived, &oldRaw, &oldName); err != nil {
			return err
		}
		if archived {
			return problem(409, "policy_archived", "策略已归档，请先恢复")
		}
		var before PolicySettings
		if err := json.Unmarshal(oldRaw, &before); err != nil {
			return err
		}
		for key, value := range policyChangeSummary(before, cfg) {
			details[key] = value
		}
		if oldName != name {
			details["name"] = map[string]string{"before": oldName, "after": name}
		}
		if err := validateBindings(ctx, tx, cfg, enabled); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, "UPDATE audit_policies SET name=$1,config=$2,revision=revision+1,updated_at=NOW() WHERE id=$3 AND revision=$4", name, string(raw), id, revision)
		if err != nil {
			return err
		}
		n, _ := result.RowsAffected()
		if n != 1 {
			return ErrConflict
		}
		return nil
	}, details)
}
func (s *Store) SetPolicyState(ctx context.Context, actor, id string, revision int64, enabled bool) error {
	return s.mutate(ctx, actor, "policy.state", id, func(tx *sql.Tx) error {
		p, err := scanPolicy(tx.QueryRowContext(ctx, "SELECT "+policyColumns+" FROM audit_policies WHERE id=$1 FOR UPDATE", id))
		if err != nil {
			return err
		}
		if p.Revision != revision {
			return ErrConflict
		}
		if p.Archived {
			return problem(409, "policy_archived", "策略已归档，请先恢复")
		}
		if enabled {
			if err := validateBindings(ctx, tx, p.Config, true); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, "UPDATE audit_policies SET enabled=$1,revision=revision+1,updated_at=NOW() WHERE id=$2", enabled, id)
		return err
	})
}
func (s *Store) Credentials(ctx context.Context) ([]Credential, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT id,name,masked,active,provider,base_url FROM provider_credentials ORDER BY name,id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Credential{}
	for rows.Next() {
		var c Credential
		if err = rows.Scan(&c.ID, &c.Name, &c.Masked, &c.Active, &c.Provider, &c.BaseURL); err != nil {
			return nil, err
		}
		items = append(items, c)
	}
	return items, rows.Err()
}
func (s *Store) SaveCredential(ctx context.Context, actor, id, name, key string, active bool) (string, error) {
	return s.SaveProviderCredential(ctx, actor, id, name, key, active, ProviderDeepSeek, "https://api.deepseek.com")
}
func (s *Store) SaveProviderCredential(ctx context.Context, actor, id, name, key string, active bool, provider, baseURL string) (string, error) {
	if provider == "" {
		provider = ProviderDeepSeek
	}
	if baseURL == "" {
		baseURL = "https://api.deepseek.com"
	}
	if err := validateProviderURL(provider, baseURL); err != nil {
		return "", problem(400, "invalid_connection", err.Error())
	}
	baseURL = providerRoot(baseURL)

	if key != "" && (len(key) < 8 || len(key) > 512 || strings.ContainsAny(key, "\r\n\t ")) {
		return "", problem(400, "invalid_credential", "密钥格式无效")
	}
	create := id == ""
	if create {
		id = randomToken("cred_")
	}
	if create && key == "" {
		return "", problem(400, "key_required", "请输入 DeepSeek API Key")
	}
	err := s.mutate(ctx, actor, "credential.update", id, func(tx *sql.Tx) error {
		if !create {
			var oldProvider, oldURL string
			err := tx.QueryRowContext(ctx, "SELECT provider,base_url FROM provider_credentials WHERE id=$1 FOR UPDATE", id).Scan(&oldProvider, &oldURL)
			if err != nil {
				return err
			}
			if oldProvider != provider || providerRoot(oldURL) != baseURL {
				return problem(400, "credential_binding_immutable", "修改供应商或连接地址需创建新凭证，防止原密钥发送到其他目标")
			}
		}
		if create {
			_, err := tx.ExecContext(ctx, "INSERT INTO provider_credentials(id,name,masked,encrypted,active,provider,base_url) VALUES($1,$2,$3,$4,$5,$6,$7)", id, name, "••••"+key[len(key)-4:], s.Vault.Seal(key, "credential:"+id), active, provider, baseURL)
			return err
		}
		var result sql.Result
		var err error
		if key != "" {
			result, err = tx.ExecContext(ctx, "UPDATE provider_credentials SET name=$1,masked=$2,encrypted=$3,active=$4 WHERE id=$5", name, "••••"+key[len(key)-4:], s.Vault.Seal(key, "credential:"+id), active, id)
		} else {
			result, err = tx.ExecContext(ctx, "UPDATE provider_credentials SET name=$1,active=$2 WHERE id=$3", name, active, id)
		}
		if err != nil {
			return err
		}
		n, _ := result.RowsAffected()
		if n != 1 {
			return ErrNotFound
		}
		_, err = tx.ExecContext(ctx, "UPDATE audit_model_channels SET revision=revision+1,cache_epoch=$1 WHERE credential_id=$2", randomToken("cache_"), id)
		return err
	})
	return id, err
}
func (s *Store) CredentialSecret(ctx context.Context, id string) (string, error) {
	var raw []byte
	err := s.DB.QueryRowContext(ctx, "SELECT encrypted FROM provider_credentials WHERE id=$1 AND active=TRUE", id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", problem(503, "credential_unavailable", "模型连接密钥尚未配置或已停用")
	}
	if err != nil {
		return "", err
	}
	return s.Vault.Open(raw, "credential:"+id)
}
func (s *Store) Keys(ctx context.Context) ([]ClientKey, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT id,name,prefix,policy_ids,rpm,active,created_at,revision,expires_at,last_used_at,rotated_at FROM client_api_keys WHERE deleted_at IS NULL ORDER BY created_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := []ClientKey{}
	for rows.Next() {
		var k ClientKey
		var raw []byte
		if err = rows.Scan(&k.ID, &k.Name, &k.Prefix, &raw, &k.RPM, &k.Active, &k.CreatedAt, &k.Revision, &k.ExpiresAt, &k.LastUsedAt, &k.RotatedAt); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &k.PolicyIDs); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}
func (s *Store) CreateKey(ctx context.Context, actor, name string, ids []string, rpm int, expires ...*time.Time) (string, error) {
	var expiry *time.Time
	if len(expires) > 0 {
		expiry = expires[0]
	}
	token := randomToken("dsa_")
	id := randomToken("key_")
	raw, _ := json.Marshal(ids)
	err := s.mutate(ctx, actor, "key.create", id, func(tx *sql.Tx) error {
		for _, policyID := range ids {
			var exists bool
			if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM audit_policies WHERE id=$1)", policyID).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return problem(400, "invalid_policy", "所选策略不存在")
			}
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO client_api_keys(id,name,prefix,token_hash,policy_ids,rpm,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7)", id, name, token[:12], digest(token), string(raw), rpm, expiry)
		return err
	})
	return token, err
}
func (s *Store) RevokeKey(ctx context.Context, actor, id string) error {
	return s.mutate(ctx, actor, "key.revoke", id, func(tx *sql.Tx) error {
		r, err := tx.ExecContext(ctx, "UPDATE client_api_keys SET active=FALSE,revision=revision+1 WHERE id=$1 AND deleted_at IS NULL", id)
		if err != nil {
			return err
		}
		n, _ := r.RowsAffected()
		if n != 1 {
			return ErrNotFound
		}
		return nil
	})
}
func (s *Store) AuthenticateKey(ctx context.Context, token string) (ClientKey, error) {
	var k ClientKey
	var raw []byte
	err := s.DB.QueryRowContext(ctx, `WITH candidate AS MATERIALIZED (SELECT id,name,policy_ids,rpm,revision FROM client_api_keys WHERE token_hash=$1 AND active=TRUE AND deleted_at IS NULL AND (expires_at IS NULL OR expires_at>NOW())), touched AS (UPDATE client_api_keys SET last_used_at=NOW() WHERE id IN (SELECT id FROM candidate) AND (last_used_at IS NULL OR last_used_at<NOW()-INTERVAL '1 minute')) SELECT id,name,policy_ids,rpm,revision FROM candidate`, digest(token)).Scan(&k.ID, &k.Name, &raw, &k.RPM, &k.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return k, problem(401, "invalid_api_key", "审核服务访问密钥无效")
	}
	if err != nil {
		return k, err
	}
	err = json.Unmarshal(raw, &k.PolicyIDs)
	return k, err
}
func (s *Store) Rate(ctx context.Context, bucket string, max int) (bool, error) {
	var hits int
	err := s.DB.QueryRowContext(ctx, `INSERT INTO rate_limits(bucket,starts_at,hits) VALUES($1,NOW(),1) ON CONFLICT(bucket) DO UPDATE SET starts_at=CASE WHEN rate_limits.starts_at < NOW()-INTERVAL '1 minute' THEN NOW() ELSE rate_limits.starts_at END,hits=CASE WHEN rate_limits.starts_at < NOW()-INTERVAL '1 minute' THEN 1 ELSE rate_limits.hits+1 END RETURNING hits`, bucket).Scan(&hits)
	return hits <= max, err
}
func (s *Store) Record(ctx context.Context, l AuditLog, text string, days int) error {
	var encrypted []byte
	if l.InputStored {
		encrypted = s.Vault.Seal(text, "input:"+l.ID)
	}
	output, outputExpiry, err := s.prepareModelOutput(&l, days)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(l)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, "INSERT INTO audit_requests(id,kind,policy_id,client_id,flagged,error_code,metadata,input_cipher,expires_at,output_cipher,output_expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)", l.ID, l.Kind, l.PolicyID, l.ClientID, l.Flagged, l.ErrorCode, string(raw), encrypted, time.Now().Add(time.Duration(days)*24*time.Hour), output, outputExpiry)
	return err
}
func (s *Store) Logs(ctx context.Context, f LogFilter) ([]AuditLog, int, error) {
	args := []any{}
	where := []string{"expires_at>NOW()"}
	add := func(expr string, value any) {
		args = append(args, value)
		where = append(where, fmt.Sprintf(expr, len(args)))
	}
	// Keep the former result=keyword_ignored link working. Missing metadata on
	// older records is treated as a regular audit, not a keyword bypass.
	if f.Result == "keyword_ignored" || f.KeywordIgnore == "only" {
		where = append(where, "metadata->>'keyword_ignored'='true'")
	} else if f.KeywordIgnore != "include" {
		where = append(where, "metadata->>'keyword_ignored' IS DISTINCT FROM 'true'")
	}
	if f.LatencyGTMS != nil {
		add("(metadata->>'latency_ms')::bigint > $%d", *f.LatencyGTMS)
	}
	if f.Kind != "" {
		add("kind=$%d", f.Kind)
	}
	if f.PolicyID != "" {
		add("policy_id=$%d", f.PolicyID)
	}
	if f.ClientID != "" {
		add("client_id=$%d", f.ClientID)
	}
	if f.RequestID != "" {
		add("id=$%d", f.RequestID)
	}
	roots, attempts := []string{}, []string{}
	for _, filter := range []struct{ value, root, attempt string }{
		{f.Model, "metadata->>'model'=$%d", "(item->>'model'=$%[1]d OR item->'usage'->>'actual_model'=$%[1]d)"},
		{f.ChannelID, "metadata->>'channel_id'=$%d", "item->>'channel_id'=$%d"},
		{f.ErrorCode, "error_code=$%d", "item->>'error_code'=$%d"},
	} {
		if filter.value == "" {
			continue
		}
		args = append(args, filter.value)
		roots = append(roots, fmt.Sprintf(filter.root, len(args)))
		attempts = append(attempts, fmt.Sprintf(filter.attempt, len(args)))
	}
	if len(roots) > 0 {
		where = append(where, "(("+strings.Join(roots, " AND ")+") OR EXISTS(SELECT 1 FROM jsonb_array_elements(CASE WHEN jsonb_typeof(metadata->'attempts')='array' THEN metadata->'attempts' ELSE '[]'::jsonb END) item WHERE "+strings.Join(attempts, " AND ")+"))")
	}
	switch f.Result {
	case "flagged":
		where = append(where, "flagged=TRUE")
	case "allow":
		where = append(where, "flagged=FALSE AND error_code=''")
	case "keyword_ignored":
		where = append(where, "metadata->>'keyword_ignored'='true' AND error_code=''")
	case "error":
		where = append(where, "error_code<>''")
	}
	if f.From != "" {
		add("created_at >= $%d::timestamptz", f.From)
	}
	if f.To != "" {
		add("created_at < $%d::timestamptz", f.To)
	}
	clause := strings.Join(where, " AND ")
	var count int
	if err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM audit_requests WHERE "+clause, args...).Scan(&count); err != nil {
		return nil, 0, err
	}
	args = append(args, f.PageSize, (f.Page-1)*f.PageSize)
	rows, err := s.DB.QueryContext(ctx, `SELECT (metadata-'model_output') || jsonb_build_object('attempts',COALESCE((SELECT jsonb_agg(item-'model_output') FROM jsonb_array_elements(CASE WHEN jsonb_typeof(metadata->'attempts')='array' THEN metadata->'attempts' ELSE '[]'::jsonb END) item),'[]'::jsonb)),created_at FROM audit_requests WHERE `+clause+fmt.Sprintf(" ORDER BY created_at DESC,id DESC LIMIT $%d OFFSET $%d", len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	logs := []AuditLog{}
	for rows.Next() {
		var raw []byte
		var created time.Time
		if err = rows.Scan(&raw, &created); err != nil {
			return nil, 0, err
		}
		var l AuditLog
		if err = json.Unmarshal(raw, &l); err != nil {
			return nil, 0, err
		}
		l.CreatedAt = created
		l.ModelOutput = ""
		for i := range l.Attempts {
			l.Attempts[i].ModelOutput = ""
		}
		logs = append(logs, l)
	}
	if err = rows.Err(); err != nil {
		return nil, 0, err
	}
	rows.Close()
	ids := make([]string, len(logs))
	for i := range logs {
		ids[i] = logs[i].ID
	}
	costs, err := s.requestCosts(ctx, ids)
	if err != nil {
		return nil, 0, err
	}
	for i := range logs {
		logs[i].Cost = keywordIgnoreCost(costs[logs[i].ID], logs[i].KeywordIgnored)
	}
	return logs, count, nil
}
func (s *Store) LogDetail(ctx context.Context, id string) (AuditLog, error) {
	var raw, cipher, output []byte
	var l AuditLog
	err := s.DB.QueryRowContext(ctx, "SELECT metadata,input_cipher,created_at,CASE WHEN output_expires_at>NOW() THEN output_cipher END FROM audit_requests WHERE id=$1 AND expires_at>NOW()", id).Scan(&raw, &cipher, &l.CreatedAt, &output)
	if errors.Is(err, sql.ErrNoRows) {
		return l, ErrNotFound
	}
	if err != nil {
		return l, err
	}
	created := l.CreatedAt
	if err = json.Unmarshal(raw, &l); err != nil {
		return l, err
	}
	l.CreatedAt = created
	if err := s.restoreModelOutput(&l, output); err != nil {
		return l, err
	}
	if len(cipher) > 0 {
		l.Input, err = s.Vault.Open(cipher, "input:"+id)
	}
	if err != nil {
		return l, err
	}
	l.Cost, err = s.requestCost(ctx, id)
	l.Cost = keywordIgnoreCost(l.Cost, l.KeywordIgnored)
	if err != nil {
		return l, err
	}
	for i := range l.Attempts {
		l.Attempts[i].Cost, err = s.Cost(ctx, l.Attempts[i].ID)
		if err != nil {
			return l, err
		}
	}
	return l, nil
}
func (s *Store) Overview(ctx context.Context) (map[string]any, error) {
	var count, hits, fail, tokens int64
	var avg, p95 float64
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*),COUNT(*) FILTER(WHERE flagged),COUNT(*) FILTER(WHERE error_code<>''),COALESCE(SUM((metadata->'usage'->>'total_tokens')::bigint),0),COALESCE(AVG((metadata->>'latency_ms')::float),0),COALESCE(percentile_cont(0.95) WITHIN GROUP(ORDER BY (metadata->>'latency_ms')::float),0) FROM audit_requests WHERE kind='production' AND created_at>NOW()-INTERVAL '24 hours' AND expires_at>NOW()`).Scan(&count, &hits, &fail, &tokens, &avg, &p95)
	return map[string]any{"requests": count, "flagged": hits, "errors": fail, "tokens": tokens, "avg_latency_ms": avg, "p95_latency_ms": p95}, err
}
func (s *Store) Cleanup(ctx context.Context) error {
	if err := s.cleanupEvaluations(ctx); err != nil {
		return err
	}
	if _, err := s.DB.ExecContext(ctx, "DELETE FROM evaluation_runs WHERE created_at<NOW()-INTERVAL '90 days' AND status NOT IN ('queued','running')"); err != nil {
		return err
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE audit_requests SET output_cipher=NULL,output_expires_at=NULL,metadata=jsonb_set(metadata,'{model_output_stored}','false'::jsonb) WHERE output_cipher IS NOT NULL AND output_expires_at<NOW()`); err != nil {
		return err
	}
	for _, query := range []string{"UPDATE audit_costs SET status='pending',note='请求结算中断，预留费用待核对' WHERE status='reserved' AND started_at<NOW()-INTERVAL '2 minutes'", "DELETE FROM assessment_cache WHERE expires_at<NOW()", "DELETE FROM audit_requests WHERE expires_at<NOW()", "DELETE FROM admin_sessions WHERE expires_at<NOW()", "DELETE FROM rate_limits WHERE starts_at<NOW()-INTERVAL '1 day'", "DELETE FROM admin_action_logs WHERE created_at<NOW()-INTERVAL '365 days'"} {
		if _, err := s.DB.ExecContext(ctx, query); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) CredentialForConfig(ctx context.Context, cfg PolicyConfig) (string, error) {
	var raw []byte
	var provider, baseURL string
	err := s.DB.QueryRowContext(ctx, "SELECT encrypted,provider,base_url FROM provider_credentials WHERE id=$1 AND active=TRUE", cfg.CredentialID).Scan(&raw, &provider, &baseURL)
	if errors.Is(err, sql.ErrNoRows) {
		return "", problem(503, "credential_unavailable", "模型连接密钥尚未配置或已停用")
	}
	if err != nil {
		return "", err
	}
	if provider != cfg.ProviderID() || providerRoot(baseURL) != providerRoot(cfg.BaseURL) {
		return "", problem(400, "credential_provider_mismatch", "凭证绑定的供应商或服务地址与策略不匹配")
	}
	return s.Vault.Open(raw, "credential:"+cfg.CredentialID)
}
