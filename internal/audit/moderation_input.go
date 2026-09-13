package audit

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

const moderationTextBodyLimit = 1 << 20
const moderationImageBodyLimit = 32 << 20

type moderationRequest struct {
	Model string          `json:"model"`
	Input json.RawMessage `json:"input"`
}

// Only the moderation endpoint accepts larger bodies. Images are discarded
// before model execution; admin endpoints retain their existing 1 MiB limit.
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
	oversized := len(raw) > moderationTextBodyLimit
	if oversized {
		// Require an actual supported image block, not just an oversized text
		// request. Validate all blocks again when extracting the model input.
		var parts []struct {
			Type string `json:"type"`
		}
		hasImage := false
		if json.Unmarshal(in.Input, &parts) == nil {
			for _, part := range parts {
				hasImage = hasImage || part.Type == "image_url"
			}
		}
		if !hasImage {
			return in, false, problem(413, "body_too_large", "纯文本请求体最多 1 MiB")
		}
	}
	return in, oversized, nil
}

func parseModerationText(raw json.RawMessage, textOnlyFallback bool) (string, error) {
	if !textOnlyFallback {
		return parseText(raw)
	}
	var parts []json.RawMessage
	if json.Unmarshal(raw, &parts) != nil || len(parts) == 0 {
		return "", problem(400, "invalid_input", "input 必须为文本内容块与图片内容块数组")
	}
	var texts []string
	for _, rawPart := range parts {
		var part struct {
			Type     string          `json:"type"`
			Text     *string         `json:"text,omitempty"`
			ImageURL json.RawMessage `json:"image_url,omitempty"`
		}
		if strictJSON(rawPart, &part) != nil {
			return "", problem(400, "invalid_input", "审核内容块格式无效")
		}
		switch part.Type {
		case "text":
			if part.Text == nil || part.ImageURL != nil {
				return "", problem(400, "invalid_input", "文本内容块格式无效")
			}
			texts = append(texts, *part.Text)
		case "image_url":
			var image struct {
				URL    string `json:"url"`
				Detail string `json:"detail,omitempty"`
			}
			if part.Text != nil || strictJSON(part.ImageURL, &image) != nil || strings.TrimSpace(image.URL) == "" {
				return "", problem(400, "invalid_input", "图片内容块格式无效")
			}
		default:
			return "", problem(400, "unsupported_input", "文本降级审核只支持跳过 image_url 图片内容块")
		}
	}
	text := strings.Join(texts, "\n")
	if strings.TrimSpace(text) == "" {
		return "", problem(400, "empty_input", "请求体超过 1 MiB，跳过图片后没有可审核文本")
	}
	return validateText(text)
}
