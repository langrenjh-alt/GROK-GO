package security

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" // RFC 6238 defaults to HMAC-SHA1 for broad authenticator compatibility.
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type TOTP struct {
	issuer string
	period time.Duration
	digits int
	skew   int
	rand   io.Reader
}

func NewTOTP(issuer string) (*TOTP, error) {
	issuer = strings.TrimSpace(issuer)
	if issuer == "" {
		return nil, errors.New("TOTP issuer is required")
	}
	return &TOTP{issuer: issuer, period: 30 * time.Second, digits: 6, skew: 1, rand: rand.Reader}, nil
}

func (t *TOTP) GenerateSecret() (string, error) {
	if t == nil {
		return "", errors.New("TOTP is not configured")
	}
	secret := make([]byte, 20)
	if _, err := io.ReadFull(t.rand, secret); err != nil {
		return "", fmt.Errorf("generate TOTP secret: %w", err)
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secret), nil
}

func (t *TOTP) ProvisioningURI(account, secret string) (string, error) {
	if _, err := decodeTOTPSecret(secret); err != nil {
		return "", err
	}
	account = strings.TrimSpace(account)
	if account == "" {
		return "", errors.New("TOTP account label is required")
	}
	label := t.issuer + ":" + account
	query := url.Values{}
	query.Set("secret", normalizeTOTPSecret(secret))
	query.Set("issuer", t.issuer)
	query.Set("algorithm", "SHA1")
	query.Set("digits", strconv.Itoa(t.digits))
	query.Set("period", strconv.Itoa(int(t.period.Seconds())))
	return "otpauth://totp/" + url.PathEscape(label) + "?" + query.Encode(), nil
}

func (t *TOTP) Validate(code, secret string, now time.Time) bool {
	if t == nil || len(code) != t.digits {
		return false
	}
	for _, char := range code {
		if char < '0' || char > '9' {
			return false
		}
	}
	key, err := decodeTOTPSecret(secret)
	if err != nil {
		return false
	}
	step := now.Unix() / int64(t.period.Seconds())
	for offset := -t.skew; offset <= t.skew; offset++ {
		if hmac.Equal([]byte(code), []byte(totpCode(key, uint64(step+int64(offset)), t.digits))) {
			return true
		}
	}
	return false
}

func normalizeTOTPSecret(secret string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(secret), " ", ""))
}

func decodeTOTPSecret(secret string) ([]byte, error) {
	decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(normalizeTOTPSecret(secret))
	if err != nil || len(decoded) < 16 {
		return nil, errors.New("invalid TOTP secret")
	}
	return decoded, nil
}

func totpCode(key []byte, counter uint64, digits int) string {
	var payload [8]byte
	binary.BigEndian.PutUint64(payload[:], counter)
	mac := hmac.New(sha1.New, key)
	_, _ = mac.Write(payload[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := (uint32(sum[offset])&0x7f)<<24 |
		uint32(sum[offset+1])<<16 |
		uint32(sum[offset+2])<<8 |
		uint32(sum[offset+3])
	modulus := uint32(1)
	for range digits {
		modulus *= 10
	}
	return fmt.Sprintf("%0*d", digits, value%modulus)
}
