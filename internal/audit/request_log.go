package audit

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// Request metadata deliberately excludes credentials, image URLs and raw bodies.
type AuditRequest struct {
	Method           string `json:"method"`
	Path             string `json:"path"`
	Model            string `json:"model,omitempty"`
	Stage            string `json:"stage"`
	HTTPStatus       int    `json:"http_status"`
	InputType        string `json:"input_type,omitempty"`
	TextChars        int    `json:"text_chars"`
	ImageCount       int    `json:"image_count"`
	TextOnlyFallback bool   `json:"text_only_fallback,omitempty"`
	InputScope       string `json:"input_scope,omitempty"`
}

type requestAuditKey struct{}
type requestAudit struct {
	log      AuditLog
	input    string
	days     int
	recorded bool
}

func (s *Server) auditModeration(next endpoint) endpoint {
	return func(w http.ResponseWriter, r *http.Request) (err error) {
		ip := requestClientIP(r, s.trustedProxies)
		releaseIngress, err := s.ingress.acquire(ip, time.Now())
		if err != nil {
			s.ingress.recordRejection(time.Now())
			return err
		}
		defer releaseIngress()
		releaseBody, err := s.admission.acquire(r.ContentLength, moderationImageBodyLimit)
		if err != nil {
			s.ingress.recordRejection(time.Now())
			return err
		}
		defer releaseBody()
		// Admission covers authentication and the final audit write. Requests
		// rejected above never touch the database or allocate audit metadata.
		a := &requestAudit{days: DefaultSettings().RetentionDays, log: AuditLog{
			ID: randomToken("audit_"), Kind: "production", CreatedAt: time.Now().UTC(),
			Attempts: []AuditAttempt{}, Usage: Usage{Reported: true},
			Request: &AuditRequest{Method: r.Method, Path: r.URL.Path, Stage: "authentication"},
		}}
		w.Header().Set("X-Audit-Request-ID", a.log.ID)
		r = r.WithContext(context.WithValue(r.Context(), requestAuditKey{}, a))
		defer func() {
			if a.recorded {
				return
			}
			a.log.LatencyMS = time.Since(a.log.CreatedAt).Milliseconds()
			a.log.Request.HTTPStatus = auditHTTPStatus(err)
			if err != nil {
				a.log.ErrorCode, a.log.ErrorMessage = errorCode(err), storedAuditError(err)
				a.log.Confidence, a.log.Flagged, a.log.Reason = nil, false, ""
			}
			// Disconnections and deadline expiry must not cancel the audit write.
			ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), time.Second)
			defer cancel()
			if recordErr := s.Store.Record(ctx, a.log, a.input, a.days); recordErr != nil {
				s.notePersistenceFailure(a.log.ID, "request_record")
				slog.Error("moderation request record failed", "request_id", a.log.ID, "stage", a.log.Request.Stage, "error_code", a.log.ErrorCode)
				if err == nil {
					err = problem(503, "record_unavailable", "审核记录暂时无法保存")
				}
			}
		}()
		return next(w, r)
	}
}

func auditHTTPStatus(err error) int {
	if err == nil {
		return http.StatusOK
	}
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Status
	}
	return http.StatusInternalServerError
}

// Retain only shape/counts by default. Text is encrypted only for an authorized
// policy with StoreInput enabled; unsupported media is never persisted.
func (a *requestAudit) describeInput(raw json.RawMessage) {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		a.log.Request.InputType = "invalid"
		return
	}
	var texts []string
	switch v := value.(type) {
	case string:
		a.log.Request.InputType = "text"
		texts = append(texts, v)
	case []any:
		a.log.Request.InputType = "content_blocks"
		for _, item := range v {
			part, ok := item.(map[string]any)
			if !ok {
				continue
			}
			typ, _ := part["type"].(string)
			if strings.Contains(typ, "image") {
				a.log.Request.ImageCount++
			}
			if typ == "text" || typ == "input_text" {
				if text, ok := part["text"].(string); ok {
					texts = append(texts, text)
				}
			}
		}
		if a.log.Request.ImageCount > 0 {
			a.log.Request.InputType = "image"
		}
	default:
		a.log.Request.InputType = "unsupported"
	}
	text := strings.Join(texts, "\n")
	a.log.Request.TextChars = utf8.RuneCountInString(text)
	if a.log.Request.TextChars > 64000 {
		text = string([]rune(text)[:64000])
	}
	a.input = text
}
