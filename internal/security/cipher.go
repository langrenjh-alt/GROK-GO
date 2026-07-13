package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
)

const cipherVersion byte = 1

var ErrInvalidCiphertext = errors.New("invalid ciphertext")

type Cipher struct {
	aead cipher.AEAD
	rand io.Reader
}

func NewCipher(key []byte) (*Cipher, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("AES-256 key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create AES cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create AES-GCM: %w", err)
	}
	return &Cipher{aead: aead, rand: rand.Reader}, nil
}

func (c *Cipher) Seal(plaintext, additionalData []byte) ([]byte, error) {
	if c == nil || c.aead == nil {
		return nil, errors.New("cipher is not configured")
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(c.rand, nonce); err != nil {
		return nil, fmt.Errorf("generate AES-GCM nonce: %w", err)
	}
	out := make([]byte, 1+len(nonce), 1+len(nonce)+len(plaintext)+c.aead.Overhead())
	out[0] = cipherVersion
	copy(out[1:], nonce)
	out = c.aead.Seal(out, nonce, plaintext, additionalData)
	return out, nil
}

func (c *Cipher) Open(ciphertext, additionalData []byte) ([]byte, error) {
	if c == nil || c.aead == nil {
		return nil, errors.New("cipher is not configured")
	}
	minimum := 1 + c.aead.NonceSize() + c.aead.Overhead()
	if len(ciphertext) < minimum || ciphertext[0] != cipherVersion {
		return nil, ErrInvalidCiphertext
	}
	nonce := ciphertext[1 : 1+c.aead.NonceSize()]
	plaintext, err := c.aead.Open(nil, nonce, ciphertext[1+c.aead.NonceSize():], additionalData)
	if err != nil {
		return nil, ErrInvalidCiphertext
	}
	return plaintext, nil
}
