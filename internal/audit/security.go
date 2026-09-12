package audit

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

func randomToken(prefix string) string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b)
}
func digest(v string) string { b := sha256.Sum256([]byte(v)); return hex.EncodeToString(b[:]) }
func hashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, 600000, 32)
	if err != nil {
		return "", err
	}
	return "pbkdf2-sha256$600000$" + base64.RawStdEncoding.EncodeToString(salt) + "$" + base64.RawStdEncoding.EncodeToString(key), nil
}
func verifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	n, err := strconv.Atoi(parts[1])
	if err != nil || n < 100000 || n > 1000000 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil || len(expected) != 32 {
		return false
	}
	actual, err := pbkdf2.Key(sha256.New, password, salt, n, 32)
	return err == nil && subtle.ConstantTimeCompare(actual, expected) == 1
}

type Vault struct {
	aead    cipher.AEAD
	hashKey []byte
}

func NewVault(encoded string) (*Vault, error) {
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(key) != 32 {
		return nil, errors.New("MASTER_KEY must be a base64-encoded 32-byte key")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	cacheKey := hmac.New(sha256.New, key)
	_, _ = cacheKey.Write([]byte("deepseek-audit/assessment-cache-key/v1"))
	return &Vault{aead: aead, hashKey: cacheKey.Sum(nil)}, err
}
func (v *Vault) Seal(text, purpose string) []byte {
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		panic(err)
	}
	return v.aead.Seal(nonce, nonce, []byte(text), []byte(purpose))
}
func (v *Vault) Open(data []byte, purpose string) (string, error) {
	n := v.aead.NonceSize()
	if len(data) < n {
		return "", errors.New("encrypted value is invalid")
	}
	b, err := v.aead.Open(nil, data[:n], data[n:], []byte(purpose))
	return string(b), err
}

// Reject duplicate keys and trailing values before typed decoding.
func strictJSON(raw []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := uniqueValue(dec, 0); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("expected one JSON value")
	}
	dec = json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(out)
}
func uniqueValue(d *json.Decoder, depth int) error {
	if depth > 32 {
		return errors.New("JSON nesting limit exceeded")
	}
	t, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			s, ok := key.(string)
			if !ok || seen[s] {
				return errors.New("duplicate JSON key")
			}
			seen[s] = true
			if err := uniqueValue(d, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := uniqueValue(d, depth+1); err != nil {
				return err
			}
		}
	default:
		return errors.New("unexpected JSON delimiter")
	}
	_, err = d.Token()
	return err
}
func ParseAssessment(raw []byte) (Assessment, error) {
	var data struct {
		Confidence *float64 `json:"confidence"`
		Reason     *string  `json:"reason"`
	}
	if err := strictJSON(raw, &data); err != nil {
		return Assessment{}, fmt.Errorf("审核结果不是符合协议的 JSON: %w", err)
	}
	if data.Confidence == nil || data.Reason == nil || *data.Confidence < 0 || *data.Confidence > 1 || utf8.RuneCountInString(*data.Reason) > 20 {
		return Assessment{}, errors.New("审核结果需包含 0～1 的 confidence 与最多 20 字的 reason")
	}
	return Assessment{*data.Confidence, *data.Reason}, nil
}
