package audit

import (
	"bytes"
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// Both modes require verified TLS; never fall back to cleartext authentication.
func sendSMTP(ctx context.Context, cfg EmailSettings, password, subject, body string) error {
	return sendSMTPWithTLS(ctx, cfg, password, subject, body, &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12})
}

func sendSMTPWithTLS(ctx context.Context, cfg EmailSettings, password, subject, body string, tlsConfig *tls.Config) error {
	if err := cfg.validate(true); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)))
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	if err = conn.SetDeadline(deadline); err != nil {
		return err
	}
	var transport net.Conn = conn
	if cfg.Security == "tls" {
		secure := tls.Client(conn, tlsConfig)
		if err = secure.HandshakeContext(ctx); err != nil {
			return err
		}
		transport = secure
	}
	client, err := smtp.NewClient(transport, cfg.Host)
	if err != nil {
		return err
	}
	defer client.Close()
	if cfg.Security == "starttls" {
		if err = client.StartTLS(tlsConfig); err != nil {
			return err
		}
	}
	if cfg.Username != "" {
		if err = client.Auth(smtp.PlainAuth("", cfg.Username, password, cfg.Host)); err != nil {
			return err
		}
	}
	if err = client.Mail(cfg.From); err != nil {
		return err
	}
	if err = client.Rcpt(cfg.To); err != nil {
		return err
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	_, err = writer.Write(emailMessage(cfg, subject, body))
	if err != nil {
		return err
	}
	if err = writer.Close(); err != nil {
		return err
	}
	// DATA acceptance is success even if the server closes before QUIT.
	_ = client.Quit()
	return nil
}

func emailMessage(cfg EmailSettings, subject, body string) []byte {
	var message bytes.Buffer
	fmt.Fprintf(&message, "From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMessage-ID: <%s@%s>\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n",
		cfg.From, cfg.To, mime.QEncoding.Encode("UTF-8", subject), time.Now().Format(time.RFC1123Z), randomToken("notice_"), cfg.From[strings.LastIndex(cfg.From, "@")+1:])
	writer := quotedprintable.NewWriter(&message)
	_, _ = writer.Write([]byte(body))
	_ = writer.Close()
	return message.Bytes()
}

func hitEmail(l AuditLog, origin string) (string, string) {
	confidence := "未知"
	if l.Confidence != nil {
		confidence = strconv.FormatFloat(*l.Confidence, 'f', -1, 64)
	}
	// Never include raw input, images, model output, or caller credentials.
	return "内容审核命中提醒", fmt.Sprintf("正式审核发现命中，请登录管理后台查看审核记录。\n\n请求 ID：%s\n时间：%s\n策略 ID：%s\n调用方 ID：%s\n模型：%s\n模型评分：%s\n命中阈值：%g\n原因：%s\n结果缓存：%t\n\n管理后台：%s\n在“审核记录”中按请求 ID 查找详情。",
		l.ID, l.CreatedAt.In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04:05 +08:00"), l.PolicyID, l.ClientID, l.Model, confidence, l.Threshold, redactReason(l.Reason), l.CacheHit, origin)
}

// StartEmailWorker runs one bounded SMTP delivery at a time. The durable queue
// survives restarts; a two-minute lease also permits recovery of interrupted sends.
func (s *Server) StartEmailWorker() {
	s.evaluationMu.Lock()
	defer s.evaluationMu.Unlock()
	if s.closing || s.emailStarted {
		return
	}
	s.emailStarted = true
	s.emailWorkers.Add(1)
	go func() {
		defer s.emailWorkers.Done()
		for s.backgroundContext.Err() == nil {
			ctx, cancel := context.WithTimeout(s.backgroundContext, 20*time.Second)
			worked, err := s.deliverEmail(ctx, sendSMTP)
			cancel()
			if err != nil && s.backgroundContext.Err() == nil {
				slog.Error("email notification queue failed")
			}
			if worked && err == nil {
				continue
			}
			timer := time.NewTimer(2 * time.Second)
			select {
			case <-s.backgroundContext.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
}

type emailSender func(context.Context, EmailSettings, string, string, string) error

func (s *Server) deliverEmail(ctx context.Context, send emailSender) (bool, error) {
	cfg, cipher, err := s.Store.emailSettings(ctx)
	if err != nil || !cfg.Enabled {
		return false, err
	}
	var id string
	var attempts int
	var raw []byte
	err = s.Store.DB.QueryRowContext(ctx, `WITH candidate AS (
        SELECT n.request_id FROM email_notifications n JOIN audit_requests a ON a.id=n.request_id
        WHERE n.sent_at IS NULL AND n.attempts<5 AND n.next_attempt_at<=NOW() AND a.expires_at>NOW()
        ORDER BY n.next_attempt_at,n.request_id LIMIT 1 FOR UPDATE OF n SKIP LOCKED
    ), claimed AS (
        UPDATE email_notifications n SET attempts=attempts+1,next_attempt_at=NOW()+INTERVAL '2 minutes'
        FROM candidate c WHERE n.request_id=c.request_id RETURNING n.request_id,n.attempts
    ) SELECT c.request_id,c.attempts,a.metadata FROM claimed c JOIN audit_requests a ON a.id=c.request_id`).Scan(&id, &attempts, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var l AuditLog
	sendErr := json.Unmarshal(raw, &l)
	password := ""
	if sendErr == nil && len(cipher) > 0 {
		password, sendErr = s.Store.Vault.Open(cipher, "email:smtp-password")
	}
	if sendErr == nil {
		subject, body := hitEmail(l, s.Origin)
		sendErr = send(ctx, cfg.EmailSettings, password, subject, body)
	}
	// Do not save provider errors: they can contain addresses or credentials.
	finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if sendErr == nil {
		_, err = s.Store.DB.ExecContext(finish, "UPDATE email_notifications SET sent_at=NOW(),last_error='' WHERE request_id=$1 AND attempts=$2", id, attempts)
	} else {
		slog.Warn("email notification send failed", "request_id", id, "attempt", attempts)
		_, err = s.Store.DB.ExecContext(finish, "UPDATE email_notifications SET last_error='email_send_failed',next_attempt_at=$3 WHERE request_id=$1 AND attempts=$2", id, attempts, time.Now().Add(time.Duration(attempts*attempts)*time.Minute))
	}
	return true, err
}
