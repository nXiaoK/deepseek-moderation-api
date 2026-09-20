package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"net"
	"net/http"
	"net/mail"
	"strings"
	"time"
)

type EmailSettings struct {
	Enabled  bool   `json:"enabled"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Security string `json:"security"`
	Username string `json:"username"`
	From     string `json:"from"`
	To       string `json:"to"`
}

type emailSettingsView struct {
	EmailSettings
	Revision    int64 `json:"revision"`
	PasswordSet bool  `json:"password_set"`
}

func (c EmailSettings) validate(required bool) error {
	invalid := func(message string) error { return problem(400, "invalid_email_settings", message) }
	if c.Port < 1 || c.Port > 65535 || (c.Security != "tls" && c.Security != "starttls") {
		return invalid("请配置有效 SMTP 端口和 TLS / STARTTLS 加密方式")
	}
	if len(c.Host) > 253 || strings.ContainsAny(c.Host, "\r\n\t /\\@?#") || strings.Contains(c.Host, ":") && net.ParseIP(c.Host) == nil {
		return invalid("SMTP 主机只填写域名或 IP，不含协议或端口")
	}
	if len(c.Username) > 320 || strings.ContainsAny(c.Username, "\r\n\x00") {
		return invalid("SMTP 用户名无效")
	}
	for _, address := range []string{c.From, c.To} {
		if address == "" && !required {
			continue
		}
		parsed, err := mail.ParseAddress(address)
		if err != nil || parsed.Address != address || len(address) > 320 || strings.ContainsAny(address, "\r\n") {
			return invalid("发件邮箱和站长收件邮箱需填写单个完整邮箱地址")
		}
		for _, r := range address {
			if r > 127 {
				return invalid("邮箱地址需使用 ASCII 字符")
			}
		}
	}
	if required && c.Host == "" {
		return invalid("请填写 SMTP 主机")
	}
	return nil
}

func (s *Store) emailSettings(ctx context.Context) (emailSettingsView, []byte, error) {
	var v emailSettingsView
	var raw, cipher []byte
	err := s.DB.QueryRowContext(ctx, "SELECT revision,config,password_cipher FROM email_settings WHERE id=TRUE").Scan(&v.Revision, &raw, &cipher)
	if err == nil {
		err = json.Unmarshal(raw, &v.EmailSettings)
	}
	v.PasswordSet = len(cipher) > 0
	return v, cipher, err
}

func (s *Server) getEmailSettings(w http.ResponseWriter, r *http.Request) error {
	v, _, err := s.Store.emailSettings(r.Context())
	if err != nil {
		return err
	}
	return writeJSON(w, 200, v)
}

func (s *Server) saveEmailSettings(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		EmailSettings
		ExpectedRevision int64  `json:"expected_revision"`
		Password         string `json:"password"`
		ClearPassword    bool   `json:"clear_password"`
	}
	if err := readJSON(w, r, &in); err != nil {
		return err
	}
	in.Host, in.Username = strings.TrimSpace(in.Host), strings.TrimSpace(in.Username)
	in.From, in.To = strings.TrimSpace(in.From), strings.TrimSpace(in.To)
	if err := in.EmailSettings.validate(in.Enabled); err != nil {
		return err
	}
	if len(in.Password) > 1024 || strings.ContainsAny(in.Password, "\r\n\x00") || in.ClearPassword && in.Password != "" {
		return problem(400, "invalid_email_settings", "SMTP 密码无效，不能同时更换和清除密码")
	}
	err := s.Store.mutate(r.Context(), actor(r), "settings.email", "email", func(tx *sql.Tx) error {
		var previous EmailSettings
		var revision int64
		var raw, cipher []byte
		if err := tx.QueryRowContext(r.Context(), "SELECT revision,config,password_cipher FROM email_settings WHERE id=TRUE FOR UPDATE").Scan(&revision, &raw, &cipher); err != nil {
			return err
		}
		if revision != in.ExpectedRevision {
			return ErrConflict
		}
		if err := json.Unmarshal(raw, &previous); err != nil {
			return err
		}
		if in.ClearPassword {
			cipher = nil
		}
		if in.Password != "" {
			cipher = s.Store.Vault.Seal(in.Password, "email:smtp-password")
		} else if len(cipher) > 0 && (previous.Host != in.Host || previous.Username != in.Username) {
			return problem(400, "invalid_email_settings", "更换 SMTP 主机或用户名时，请重新填写或清除密码")
		}
		if in.Enabled && in.Username != "" && len(cipher) == 0 {
			return problem(400, "invalid_email_settings", "使用 SMTP 认证时请填写密码或授权码")
		}
		raw, err := json.Marshal(in.EmailSettings)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(r.Context(), "UPDATE email_settings SET config=$1,password_cipher=$2,revision=revision+1 WHERE id=TRUE", string(raw), cipher); err != nil {
			return err
		}
		if !in.Enabled {
			_, err = tx.ExecContext(r.Context(), "DELETE FROM email_notifications WHERE sent_at IS NULL")
		}
		return err
	})
	if err != nil {
		return err
	}
	return s.getEmailSettings(w, r)
}

func (s *Server) testEmailSettings(w http.ResponseWriter, r *http.Request) error {
	ok, err := s.Store.Rate(r.Context(), "email:test", 3)
	if err != nil {
		return err
	}
	if !ok {
		return problem(429, "rate_limited", "测试邮件每分钟最多发送 3 次")
	}
	v, cipher, err := s.Store.emailSettings(r.Context())
	if err != nil {
		return err
	}
	if err = v.EmailSettings.validate(true); err != nil {
		return err
	}
	password := ""
	if len(cipher) > 0 {
		password, err = s.Store.Vault.Open(cipher, "email:smtp-password")
		if err != nil {
			return err
		}
	}
	if v.Username != "" && password == "" {
		return problem(400, "invalid_email_settings", "请先保存 SMTP 密码或授权码")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err = sendSMTP(ctx, v.EmailSettings, password, "邮件提醒测试", "这是一封站长邮件提醒测试。SMTP 配置可以正常发送邮件。\n管理后台："+s.Origin); err != nil {
		return problem(502, "email_send_failed", "测试邮件发送失败，请检查 SMTP 地址、端口、加密方式、授权码及收发件邮箱")
	}
	return writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) emailStatus(w http.ResponseWriter, r *http.Request) error {
	var v struct {
		Pending    int        `json:"pending"`
		Failed     int        `json:"failed"`
		Sent       int        `json:"sent"`
		LastSentAt *time.Time `json:"last_sent_at"`
	}
	err := s.Store.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FILTER(WHERE sent_at IS NULL AND attempts<5),COUNT(*) FILTER(WHERE sent_at IS NULL AND attempts>=5),COUNT(*) FILTER(WHERE sent_at IS NOT NULL),MAX(sent_at) FROM email_notifications`).Scan(&v.Pending, &v.Failed, &v.Sent, &v.LastSentAt)
	if err != nil {
		return err
	}
	return writeJSON(w, 200, v)
}
