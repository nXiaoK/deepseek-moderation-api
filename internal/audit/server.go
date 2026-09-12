package audit

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

type Server struct {
	Store         *Store
	Engine        *Engine
	Origin        string
	Secure        bool
	StaticDir     string
	dummyPassword string
}
type session struct {
	Username  string
	CSRF      string
	TokenHash string
}
type sessionKey struct{}
type endpoint func(http.ResponseWriter, *http.Request) error

func NewServer(store *Store, origin, static string) (*Server, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.Path != "" || u.RawQuery != "" || u.User != nil || u.Fragment != "" {
		return nil, errors.New("PUBLIC_URL must be an origin without a path")
	}
	if u.Scheme == "http" && u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" && u.Hostname() != "::1" {
		return nil, errors.New("PUBLIC_URL requires HTTPS except on localhost")
	}
	hash, err := hashPassword(randomToken("dummy"))
	if err != nil {
		return nil, err
	}
	return &Server{Store: store, Engine: NewEngine(16), Origin: origin, Secure: u.Scheme == "https", StaticDir: static, dummyPassword: hash}, nil
}
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.wrap(func(w http.ResponseWriter, r *http.Request) error {
		return writeJSON(w, 200, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("GET /readyz", s.wrap(func(w http.ResponseWriter, r *http.Request) error {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if err := s.Store.DB.PingContext(ctx); err != nil {
			return problem(503, "not_ready", "数据库不可用")
		}
		return writeJSON(w, 200, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("POST /admin/auth/login", s.wrap(s.login))
	admin := func(pattern string, h endpoint) { mux.HandleFunc(pattern, s.wrap(s.admin(h))) }
	admin("GET /admin/session", func(w http.ResponseWriter, r *http.Request) error {
		v := r.Context().Value(sessionKey{}).(session)
		return writeJSON(w, 200, map[string]string{"username": v.Username, "csrf": v.CSRF})
	})
	admin("POST /admin/auth/logout", s.logout)
	admin("PUT /admin/auth/password", s.changePassword)
	admin("GET /admin/policies", s.listPolicies)
	admin("POST /admin/policies", s.createPolicy)
	admin("GET /admin/policies/{id}", s.getPolicy)
	admin("PUT /admin/policies/{id}/draft", s.saveDraft)
	admin("PUT /admin/policies/{id}/state", s.policyState)
	admin("POST /admin/policies/{id}/publish", s.publish)
	admin("POST /admin/policies/{id}/rollback", s.publish)
	admin("GET /admin/policies/{id}/versions", s.versions)
	admin("POST /admin/policies/{id}/test", s.testPolicy)
	admin("GET /admin/credentials", s.credentials)
	admin("POST /admin/credentials", s.saveCredential)
	admin("PUT /admin/credentials/{id}", s.saveCredential)
	admin("GET /admin/api-keys", s.keys)
	admin("POST /admin/api-keys", s.createKey)
	admin("POST /admin/api-keys/{id}/revoke", s.revokeKey)
	admin("GET /admin/audit-logs", s.logs)
	admin("GET /admin/audit-logs/{id}", s.logDetail)
	admin("GET /admin/overview", func(w http.ResponseWriter, r *http.Request) error {
		data, err := s.Store.Overview(r.Context())
		if err != nil {
			return err
		}
		return writeJSON(w, 200, data)
	})
	admin("GET /admin/actions", s.actions)
	admin("GET /admin/billing/prices", s.prices)
	admin("POST /admin/billing/prices", s.savePrice)
	admin("GET /admin/billing/costs", s.costs)
	admin("POST /admin/billing/costs/{id}/reconcile", s.reconcileCost)
	admin("GET /admin/billing/budgets", s.budgets)
	admin("PUT /admin/api-keys/{id}/budget", s.saveBudget)
	mux.HandleFunc("POST /v1/moderations", s.wrap(s.moderate))
	mux.HandleFunc("GET /v1/models", s.wrap(s.models))
	mux.HandleFunc("/", s.static)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; object-src 'none'; frame-ancestors 'none'; base-uri 'none'")
		w.Header().Set("Cache-Control", "no-store")
		mux.ServeHTTP(w, r)
	})
}
func (s *Server) wrap(fn endpoint) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
		defer cancel()
		r = r.WithContext(ctx)
		if err := fn(w, r); err != nil {
			ae := &APIError{500, "internal_error", "服务暂时不可用"}
			var custom *APIError
			if errors.As(err, &custom) {
				ae = custom
			} else {
				slog.Error("request failed", "method", r.Method, "path", r.URL.Path)
			}
			if ae.Status == 429 {
				w.Header().Set("Retry-After", "60")
			}
			_ = writeJSON(w, ae.Status, map[string]any{"error": map[string]string{"code": ae.Code, "message": ae.Message, "type": "audit_service_error"}})
		}
	}
}
func writeJSON(w http.ResponseWriter, status int, v any) error {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(v)
}
func readJSON(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1024*1024)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return problem(413, "body_too_large", "请求体最多 1 MiB")
	}
	if err = strictJSON(raw, v); err != nil {
		return problem(400, "invalid_json", "请求 JSON 或字段不符合接口要求")
	}
	return nil
}
func actor(r *http.Request) string              { return r.Context().Value(sessionKey{}).(session).Username }
func (s *Server) originOK(r *http.Request) bool { return r.Header.Get("Origin") == s.Origin }
func (s *Server) admin(next endpoint) endpoint {
	return func(w http.ResponseWriter, r *http.Request) error {
		cookie, err := r.Cookie("audit_session")
		if err != nil {
			return problem(401, "login_required", "请先登录")
		}
		var v session
		v.TokenHash = digest(cookie.Value)
		err = s.Store.DB.QueryRowContext(r.Context(), "SELECT username,csrf FROM admin_sessions WHERE token_hash=$1 AND expires_at>NOW()", v.TokenHash).Scan(&v.Username, &v.CSRF)
		if errors.Is(err, sql.ErrNoRows) {
			return problem(401, "login_required", "登录已过期")
		}
		if err != nil {
			return err
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			if !s.originOK(r) || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(v.CSRF)) != 1 {
				return problem(403, "csrf_failed", "请求来源或会话校验失败")
			}
		}
		return next(w, r.WithContext(context.WithValue(r.Context(), sessionKey{}, v)))
	}
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) error {
	if !s.originOK(r) {
		return problem(403, "invalid_origin", "登录请求来源无效")
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	ok, err := s.Store.Rate(r.Context(), "login:"+digest(host), 10)
	if err != nil {
		return err
	}
	if !ok {
		return problem(429, "login_limited", "登录尝试过多，请稍后重试")
	}
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err = readJSON(w, r, &input); err != nil {
		return err
	}
	if len(input.Password) > 256 || len(input.Username) > 80 {
		return problem(400, "invalid_login", "登录信息长度无效")
	}
	var hash string
	err = s.Store.DB.QueryRowContext(r.Context(), "SELECT password_hash FROM admin_users WHERE username=$1", input.Username).Scan(&hash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if hash == "" {
		hash = s.dummyPassword
	}
	valid := verifyPassword(hash, input.Password)
	if err != nil || !valid {
		return problem(401, "invalid_login", "用户名或密码错误")
	}
	token := randomToken("ses_")
	csrf := randomToken("csrf_")
	if _, err = s.Store.DB.ExecContext(r.Context(), "INSERT INTO admin_sessions(token_hash,username,csrf,expires_at) VALUES($1,$2,$3,$4)", digest(token), input.Username, csrf, time.Now().Add(12*time.Hour)); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{Name: "audit_session", Value: token, Path: "/", HttpOnly: true, Secure: s.Secure, SameSite: http.SameSiteStrictMode, MaxAge: 43200})
	return writeJSON(w, 200, map[string]string{"username": input.Username, "csrf": csrf})
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request) error {
	v := r.Context().Value(sessionKey{}).(session)
	if _, err := s.Store.DB.ExecContext(r.Context(), "DELETE FROM admin_sessions WHERE token_hash=$1", v.TokenHash); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{Name: "audit_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.Secure, SameSite: http.SameSiteStrictMode})
	return writeJSON(w, 200, map[string]bool{"ok": true})
}
func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Current  string `json:"current"`
		Password string `json:"password"`
	}
	if err := readJSON(w, r, &in); err != nil {
		return err
	}
	if len(in.Password) < 16 || len(in.Password) > 256 || len(in.Current) > 256 {
		return problem(400, "invalid_password", "新密码需要 16～256 个字符")
	}
	name := actor(r)
	var hash string
	if err := s.Store.DB.QueryRowContext(r.Context(), "SELECT password_hash FROM admin_users WHERE username=$1", name).Scan(&hash); err != nil {
		return err
	}
	if !verifyPassword(hash, in.Current) {
		return problem(403, "invalid_password", "当前密码错误")
	}
	next, err := hashPassword(in.Password)
	if err != nil {
		return err
	}
	err = s.Store.mutate(r.Context(), name, "admin.password", name, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(r.Context(), "UPDATE admin_users SET password_hash=$1 WHERE username=$2 AND password_hash=$3", next, name, hash)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return ErrConflict
		}
		_, err = tx.ExecContext(r.Context(), "DELETE FROM admin_sessions WHERE username=$1", name)
		return err
	})
	if err != nil {
		return err
	}
	return writeJSON(w, 200, map[string]bool{"ok": true})
}
func (s *Server) listPolicies(w http.ResponseWriter, r *http.Request) error {
	items, err := s.Store.Policies(r.Context())
	if err != nil {
		return err
	}
	return writeJSON(w, 200, items)
}
func (s *Server) getPolicy(w http.ResponseWriter, r *http.Request) error {
	p, err := s.Store.Policy(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	return writeJSON(w, 200, p)
}
func (s *Server) createPolicy(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Name   string `json:"name"`
		Alias  string `json:"alias"`
		Source string `json:"source_id"`
	}
	if err := readJSON(w, r, &in); err != nil {
		return err
	}
	if strings.TrimSpace(in.Name) == "" || len(in.Name) > 200 || !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,79}$`).MatchString(in.Alias) {
		return problem(400, "invalid_policy", "请输入策略名称和有效的模型别名")
	}
	p, err := s.Store.CreatePolicy(r.Context(), actor(r), in.Name, in.Alias, in.Source)
	if err != nil {
		return err
	}
	return writeJSON(w, 201, p)
}
func (s *Server) saveDraft(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Revision int64        `json:"expected_revision"`
		Name     string       `json:"name"`
		Config   PolicyConfig `json:"config"`
	}
	if err := readJSON(w, r, &in); err != nil {
		return err
	}
	if strings.TrimSpace(in.Name) == "" || len(in.Name) > 200 {
		return problem(400, "invalid_name", "策略名称不能为空且最多 200 字节")
	}
	if err := s.Store.SaveDraft(r.Context(), actor(r), r.PathValue("id"), in.Name, in.Revision, in.Config); err != nil {
		return err
	}
	return s.getPolicy(w, r)
}
func (s *Server) policyState(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Enabled *bool `json:"enabled"`
	}
	if err := readJSON(w, r, &in); err != nil {
		return err
	}
	if in.Enabled == nil {
		return problem(400, "invalid_state", "enabled 必须为布尔值")
	}
	id := r.PathValue("id")
	err := s.Store.mutate(r.Context(), actor(r), "policy.state", id, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(r.Context(), "UPDATE audit_policies SET enabled=$1 WHERE id=$2", *in.Enabled, id)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		return err
	}
	return s.getPolicy(w, r)
}
func (s *Server) publish(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Revision int64 `json:"expected_revision"`
		Version  int   `json:"version"`
	}
	if err := readJSON(w, r, &in); err != nil {
		return err
	}
	rollback := strings.HasSuffix(r.URL.Path, "/rollback")
	if rollback && in.Version < 1 || !rollback && in.Version != 0 {
		return problem(400, "invalid_version", "版本参数无效")
	}
	version, err := s.Store.Publish(r.Context(), actor(r), r.PathValue("id"), in.Revision, in.Version)
	if err != nil {
		return err
	}
	return writeJSON(w, 200, map[string]int{"version": version})
}
func (s *Server) versions(w http.ResponseWriter, r *http.Request) error {
	items, err := s.Store.Versions(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	return writeJSON(w, 200, items)
}
func (s *Server) testPolicy(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Input    string `json:"input"`
		Source   string `json:"source"`
		Revision int64  `json:"expected_revision"`
	}
	if err := readJSON(w, r, &in); err != nil {
		return err
	}
	text, err := validateText(in.Input)
	if err != nil {
		return err
	}
	ok, err := s.Store.Rate(r.Context(), "test:"+actor(r), 30)
	if err != nil {
		return err
	}
	if !ok {
		return problem(429, "rate_limited", "试跑次数过多")
	}
	p, err := s.Store.Policy(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	cfg := p.Draft
	version := 0
	switch in.Source {
	case "draft":
		if in.Revision != p.DraftRevision {
			return ErrConflict
		}
	case "published":
		if p.Active == nil {
			return problem(400, "not_published", "该策略尚未发布")
		}
		cfg = *p.Active
		version = p.ActiveVersion
	default:
		return problem(400, "invalid_source", "请选择草稿或已发布版本")
	}
	response, err := s.runAudit(r.Context(), p, cfg, version, actor(r), "test", text)
	if err != nil {
		return err
	}
	return writeJSON(w, 200, response)
}
func (s *Server) credentials(w http.ResponseWriter, r *http.Request) error {
	items, err := s.Store.Credentials(r.Context())
	if err != nil {
		return err
	}
	return writeJSON(w, 200, items)
}
func (s *Server) saveCredential(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Name   string `json:"name"`
		Key    string `json:"api_key"`
		Active bool   `json:"active"`
	}
	if err := readJSON(w, r, &in); err != nil {
		return err
	}
	if strings.TrimSpace(in.Name) == "" || len(in.Name) > 200 || in.Key != "" && (len(in.Key) < 8 || len(in.Key) > 512 || strings.ContainsAny(in.Key, "\r\n\t ")) {
		return problem(400, "invalid_credential", "密钥名称或格式无效")
	}
	id, err := s.Store.SaveCredential(r.Context(), actor(r), r.PathValue("id"), in.Name, in.Key, in.Active)
	if err != nil {
		return err
	}
	return writeJSON(w, 200, map[string]string{"id": id})
}
func (s *Server) keys(w http.ResponseWriter, r *http.Request) error {
	items, err := s.Store.Keys(r.Context())
	if err != nil {
		return err
	}
	return writeJSON(w, 200, items)
}
func (s *Server) createKey(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Name      string   `json:"name"`
		PolicyIDs []string `json:"policy_ids"`
		RPM       int      `json:"rpm"`
	}
	if err := readJSON(w, r, &in); err != nil {
		return err
	}
	if strings.TrimSpace(in.Name) == "" || len(in.Name) > 200 || len(in.PolicyIDs) == 0 || len(in.PolicyIDs) > 100 || in.RPM < 1 || in.RPM > 10000 {
		return problem(400, "invalid_key_config", "请输入名称、允许的策略和每分钟限额（1～10000）")
	}
	token, err := s.Store.CreateKey(r.Context(), actor(r), in.Name, in.PolicyIDs, in.RPM)
	if err != nil {
		return err
	}
	return writeJSON(w, 201, map[string]string{"token": token})
}
func (s *Server) revokeKey(w http.ResponseWriter, r *http.Request) error {
	if err := s.Store.RevokeKey(r.Context(), actor(r), r.PathValue("id")); err != nil {
		return err
	}
	return writeJSON(w, 200, map[string]bool{"ok": true})
}
func (s *Server) client(r *http.Request) (ClientKey, error) {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") || len(header) > 256 {
		return ClientKey{}, problem(401, "invalid_api_key", "需要审核服务 Bearer 密钥")
	}
	return s.Store.AuthenticateKey(r.Context(), strings.TrimPrefix(header, "Bearer "))
}
func (s *Server) moderate(w http.ResponseWriter, r *http.Request) error {
	k, err := s.client(r)
	if err != nil {
		return err
	}
	ok, err := s.Store.Rate(r.Context(), "api:"+k.ID, k.RPM)
	if err != nil {
		return err
	}
	if !ok {
		return problem(429, "rate_limited", "调用额度已达上限")
	}
	var in struct {
		Model string          `json:"model"`
		Input json.RawMessage `json:"input"`
	}
	if err = readJSON(w, r, &in); err != nil {
		return err
	}
	text, err := parseText(in.Input)
	if err != nil {
		return err
	}
	p, err := s.Store.PolicyByAlias(r.Context(), in.Model)
	if err != nil {
		return err
	}
	if !slices.Contains(k.PolicyIDs, p.ID) {
		return problem(403, "policy_forbidden", "该密钥无权调用此策略")
	}
	if !p.Enabled || p.Active == nil {
		return problem(503, "policy_unavailable", "审核策略已停用或尚未发布")
	}
	res, err := s.runAudit(r.Context(), p, *p.Active, p.ActiveVersion, k.ID, "production", text)
	if err != nil {
		return err
	}
	w.Header().Set("X-Audit-Request-ID", res.ID)
	w.Header().Set("X-Audit-Policy-Version", strconv.Itoa(p.ActiveVersion))
	return writeJSON(w, 200, res)
}
func (s *Server) models(w http.ResponseWriter, r *http.Request) error {
	k, err := s.client(r)
	if err != nil {
		return err
	}
	policies, err := s.Store.Policies(r.Context())
	if err != nil {
		return err
	}
	items := []map[string]string{}
	for _, p := range policies {
		if p.Enabled && p.ActiveVersion > 0 && slices.Contains(k.PolicyIDs, p.ID) {
			items = append(items, map[string]string{"id": p.Alias, "object": "model", "owned_by": "audit-service"})
		}
	}
	return writeJSON(w, 200, map[string]any{"object": "list", "data": items})
}
func (s *Server) logs(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	size, _ := strconv.Atoi(q.Get("page_size"))
	if size < 1 || size > 100 {
		size = 20
	}
	for _, key := range []string{"from", "to"} {
		if q.Get(key) != "" {
			if _, err := time.Parse(time.RFC3339, q.Get(key)); err != nil {
				return problem(400, "invalid_date", "时间格式必须为 RFC3339")
			}
		}
	}
	logs, total, err := s.Store.Logs(r.Context(), LogFilter{page, size, q.Get("kind"), q.Get("policy_id"), q.Get("client_id"), q.Get("result"), q.Get("from"), q.Get("to")})
	if err != nil {
		return err
	}
	return writeJSON(w, 200, map[string]any{"items": logs, "total": total, "page": page, "page_size": size})
}
func (s *Server) logDetail(w http.ResponseWriter, r *http.Request) error {
	item, err := s.Store.LogDetail(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	return writeJSON(w, 200, item)
}
func (s *Server) actions(w http.ResponseWriter, r *http.Request) error {
	rows, err := s.Store.DB.QueryContext(r.Context(), "SELECT username,action,resource_id,created_at FROM admin_action_logs ORDER BY id DESC LIMIT 100")
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var user, action, id string
		var at time.Time
		if err = rows.Scan(&user, &action, &id, &at); err != nil {
			return err
		}
		items = append(items, map[string]any{"username": user, "action": action, "resource_id": id, "created_at": at})
	}
	if err = rows.Err(); err != nil {
		return err
	}
	return writeJSON(w, 200, items)
}
func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/admin/") || strings.HasPrefix(r.URL.Path, "/v1/") {
		http.NotFound(w, r)
		return
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		http.Error(w, "method not allowed", 405)
		return
	}
	name := filepath.Clean("/" + r.URL.Path)
	full := filepath.Join(s.StaticDir, name)
	if info, err := os.Stat(full); err == nil && !info.IsDir() {
		http.ServeFile(w, r, full)
		return
	}
	index := filepath.Join(s.StaticDir, "index.html")
	if _, err := os.Stat(index); err != nil {
		http.Error(w, "Build the admin frontend first: cd frontend && pnpm build", 503)
		return
	}
	http.ServeFile(w, r, index)
}

var secretPattern = regexp.MustCompile(`(?i)(?:sk-[a-z0-9_-]+|bearer\s+[^\s]+|[a-z0-9._%+-]+@[a-z0-9.-]+\.[a-z]{2,}|\b\d{11,}\b)`)

func redact(s string) string { return secretPattern.ReplaceAllString(s, "[隐去]") }
func (s *Server) CleanupLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		work, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := s.Store.Cleanup(work)
		cancel()
		if err != nil && ctx.Err() == nil {
			slog.Error("retention cleanup failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
