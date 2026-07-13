package security

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

type TokenMaterial struct {
	Plaintext string
	Prefix    string
	Digest    []byte
}

type TokenManager struct {
	pepper []byte
	rand   io.Reader
}

func NewTokenManager(pepper []byte) (*TokenManager, error) {
	if len(pepper) < 32 {
		return nil, errors.New("token pepper must be at least 32 bytes")
	}
	return &TokenManager{pepper: append([]byte(nil), pepper...), rand: rand.Reader}, nil
}

func (m *TokenManager) Generate(prefix string, randomBytes int) (TokenMaterial, error) {
	if m == nil {
		return TokenMaterial{}, errors.New("token manager is not configured")
	}
	if randomBytes < 24 {
		return TokenMaterial{}, errors.New("token entropy must be at least 24 bytes")
	}
	if prefix == "" || strings.ContainsAny(prefix, " \t\r\n") {
		return TokenMaterial{}, errors.New("token prefix is invalid")
	}
	raw := make([]byte, randomBytes)
	if _, err := io.ReadFull(m.rand, raw); err != nil {
		return TokenMaterial{}, fmt.Errorf("generate token: %w", err)
	}
	plaintext := prefix + base64.RawURLEncoding.EncodeToString(raw)
	safePrefix := plaintext
	if len(safePrefix) > len(prefix)+10 {
		safePrefix = safePrefix[:len(prefix)+10]
	}
	return TokenMaterial{Plaintext: plaintext, Prefix: safePrefix, Digest: m.Digest(plaintext)}, nil
}

func (m *TokenManager) Digest(token string) []byte {
	mac := hmac.New(sha256.New, m.pepper)
	_, _ = mac.Write([]byte(token))
	return mac.Sum(nil)
}

func (m *TokenManager) Verify(token string, expected []byte) bool {
	if m == nil || token == "" || len(expected) == 0 {
		return false
	}
	return hmac.Equal(m.Digest(token), expected)
}

func GenerateID() (string, error) {
	var id [16]byte
	if _, err := io.ReadFull(rand.Reader, id[:]); err != nil {
		return "", fmt.Errorf("generate ID: %w", err)
	}
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(id[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:], nil
}

func GenerateClientAPIKey(manager *TokenManager) (TokenMaterial, error) {
	return manager.Generate("grok_", 32)
}
