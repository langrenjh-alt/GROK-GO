package media

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var ErrInvalidSignature = errors.New("invalid or expired media signature")

type Signer struct {
	key []byte
	ttl time.Duration
	now func() time.Time
}

func NewSigner(key []byte, ttl time.Duration) (*Signer, error) {
	if len(key) < 32 {
		return nil, errors.New("media signing key must contain at least 32 bytes")
	}
	if ttl <= 0 {
		ttl = time.Hour
	}
	return &Signer{key: append([]byte(nil), key...), ttl: ttl, now: time.Now}, nil
}

func (s *Signer) SignedURL(baseURL, objectID string) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	if !validObjectID(objectID) {
		return "", errors.New("invalid media object id")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/" + url.PathEscape(objectID)
	expires := s.now().Add(s.ttl).Unix()
	query := parsed.Query()
	query.Set("exp", strconv.FormatInt(expires, 10))
	query.Set("sig", s.Sign(objectID, expires))
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func (s *Signer) Sign(objectID string, expiresUnix int64) string {
	mac := hmac.New(sha256.New, s.key)
	_, _ = mac.Write([]byte(objectID))
	_, _ = mac.Write([]byte("\n"))
	_, _ = mac.Write([]byte(strconv.FormatInt(expiresUnix, 10)))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Signer) Verify(objectID string, expiresUnix int64, signature string) error {
	if !validObjectID(objectID) || expiresUnix < s.now().Unix() {
		return ErrInvalidSignature
	}
	provided, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil {
		return ErrInvalidSignature
	}
	expected, _ := base64.RawURLEncoding.DecodeString(s.Sign(objectID, expiresUnix))
	if !hmac.Equal(provided, expected) {
		return ErrInvalidSignature
	}
	return nil
}

func validObjectID(value string) bool {
	if len(value) < 8 || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_') {
			return false
		}
	}
	return true
}
