package audit

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"
)

const moderationTextBodyLimit = 1 << 20
const moderationImageBodyLimit = 32 << 20

type moderationRequest struct {
	Model string          `json:"model"`
	Input json.RawMessage `json:"input"`
}

// Only the moderation endpoint accepts larger bodies. The returned flag means
// image blocks are present; permission to discard them comes from the channel.
func readModerationRequest(w http.ResponseWriter, r *http.Request) (moderationRequest, bool, error) {
	var in moderationRequest
	r.Body = http.MaxBytesReader(w, r.Body, moderationImageBodyLimit)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return in, false, problem(413, "body_too_large", "含图片的审核请求体最多 32 MiB")
		}
		return in, false, problem(400, "invalid_input", "无法读取审核请求体")
	}
	if err := strictJSON(raw, &in); err != nil {
		return in, false, problem(400, "invalid_json", "请求 JSON 或字段不符合接口要求")
	}
	var parts []struct {
		Type string `json:"type"`
	}
	hasImage := false
	if json.Unmarshal(in.Input, &parts) == nil {
		for _, part := range parts {
			hasImage = hasImage || part.Type == "image_url"
		}
	}
	if len(raw) > moderationTextBodyLimit && !hasImage {
		return in, false, problem(413, "body_too_large", "纯文本请求体最多 1 MiB")
	}
	return in, hasImage, nil
}

// AuditImage is kept only in memory for the lifetime of the request.
type AuditImage struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

func parseModerationInput(raw json.RawMessage) (string, []AuditImage, error) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		text, err := validateText(text)
		return text, nil, err
	}
	var parts []json.RawMessage
	if json.Unmarshal(raw, &parts) != nil || len(parts) == 0 {
		return "", nil, problem(400, "invalid_input", "input 必须为文本或文本与图片内容块数组")
	}
	var texts []string
	var images []AuditImage
	for _, rawPart := range parts {
		var part struct {
			Type     string          `json:"type"`
			Text     *string         `json:"text,omitempty"`
			ImageURL json.RawMessage `json:"image_url,omitempty"`
		}
		if strictJSON(rawPart, &part) != nil {
			return "", nil, problem(400, "invalid_input", "审核内容块格式无效")
		}
		switch part.Type {
		case "text":
			if part.Text == nil || part.ImageURL != nil {
				return "", nil, problem(400, "invalid_input", "文本内容块格式无效")
			}
			texts = append(texts, *part.Text)
		case "image_url":
			var image AuditImage
			if part.Text != nil || strictJSON(part.ImageURL, &image) != nil || !validAuditImage(image) {
				return "", nil, problem(400, "invalid_input", "图片需提供 HTTP(S) URL 或 data:image Base64，detail 仅支持 auto、low、high")
			}
			images = append(images, image)
		default:
			return "", nil, problem(400, "unsupported_input", "仅支持 text 和 image_url 内容块")
		}
	}
	text = strings.Join(texts, "\n")
	if len(images) == 0 {
		text, err := validateText(text)
		return text, nil, err
	}
	if !utf8.ValidString(text) || utf8.RuneCountInString(text) > 64000 {
		return "", nil, problem(413, "input_too_large", "审核文本最多 64000 个字符")
	}
	return text, images, nil
}

func validAuditImage(image AuditImage) bool {
	if image.Detail != "" && image.Detail != "auto" && image.Detail != "low" && image.Detail != "high" {
		return false
	}
	if strings.HasPrefix(image.URL, "data:image/") {
		header, data, ok := strings.Cut(image.URL, ",")
		return ok && strings.HasSuffix(header, ";base64") && data != ""
	}
	u, err := url.Parse(image.URL)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Hostname() != "" && u.User == nil
}

func parseModerationText(raw json.RawMessage, textOnlyFallback bool) (string, error) {
	if !textOnlyFallback {
		return parseText(raw)
	}
	text, _, err := parseModerationInput(raw)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(text) == "" {
		return "", problem(400, "empty_input", "跳过图片后没有可审核文本")
	}
	return text, nil
}

func auditInputScope(textOnly bool, images []AuditImage) string {
	if len(images) == 0 {
		return "text"
	}
	if textOnly {
		return "text_only"
	}
	return "text_and_images"
}

// URLs can change their contents without changing their address. Cache only
// embedded images, whose bytes are part of the cache identity.
func imageInputCacheable(images []AuditImage) bool {
	for _, image := range images {
		if !strings.HasPrefix(image.URL, "data:image/") {
			return false
		}
	}
	return true
}
