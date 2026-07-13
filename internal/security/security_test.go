package security

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestCipherRoundTripAndAAD(t *testing.T) {
	cipher, err := NewCipher([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("NewCipher() error = %v", err)
	}
	sealed, err := cipher.Seal([]byte("secret"), []byte("account:1"))
	if err != nil {
		t.Fatalf("Seal() error = %v", err)
	}
	opened, err := cipher.Open(sealed, []byte("account:1"))
	if err != nil || string(opened) != "secret" {
		t.Fatalf("Open() = %q, %v", opened, err)
	}
	if _, err := cipher.Open(sealed, []byte("account:2")); err != ErrInvalidCiphertext {
		t.Fatalf("Open() wrong AAD error = %v", err)
	}
	tampered := append([]byte(nil), sealed...)
	tampered[len(tampered)-1] ^= 1
	if _, err := cipher.Open(tampered, []byte("account:1")); err != ErrInvalidCiphertext {
		t.Fatalf("Open() tampered error = %v", err)
	}
}

func TestPasswordHasher(t *testing.T) {
	hasher, err := NewPasswordHasher(Argon2Params{Memory: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32})
	if err != nil {
		t.Fatalf("NewPasswordHasher() error = %v", err)
	}
	encoded, err := hasher.Hash("long enough password")
	if err != nil {
		t.Fatalf("Hash() error = %v", err)
	}
	if !strings.HasPrefix(encoded, "$argon2id$v=19$") {
		t.Fatalf("unexpected hash = %q", encoded)
	}
	ok, err := hasher.Verify("long enough password", encoded)
	if err != nil || !ok {
		t.Fatalf("Verify() = %v, %v", ok, err)
	}
	ok, err = hasher.Verify("incorrect password", encoded)
	if err != nil || ok {
		t.Fatalf("Verify(incorrect) = %v, %v", ok, err)
	}
}

func TestTokenManager(t *testing.T) {
	manager, err := NewTokenManager(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatalf("NewTokenManager() error = %v", err)
	}
	material, err := GenerateClientAPIKey(manager)
	if err != nil {
		t.Fatalf("GenerateClientAPIKey() error = %v", err)
	}
	if !strings.HasPrefix(material.Plaintext, "grok_") || !manager.Verify(material.Plaintext, material.Digest) {
		t.Fatalf("invalid token material: %+v", material)
	}
	if manager.Verify(material.Plaintext+"x", material.Digest) {
		t.Fatal("modified token verified")
	}
	id, err := GenerateID()
	if err != nil {
		t.Fatalf("GenerateID() error = %v", err)
	}
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		t.Fatalf("GenerateID() = %q", id)
	}
}

func TestTOTP(t *testing.T) {
	totp, err := NewTOTP("GROK-GO")
	if err != nil {
		t.Fatalf("NewTOTP() error = %v", err)
	}
	secret := "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP"
	now := time.Unix(1234567890, 0)
	key, _ := decodeTOTPSecret(secret)
	code := totpCode(key, uint64(now.Unix()/30), 6)
	if !totp.Validate(code, secret, now) {
		t.Fatalf("Validate(%q) = false", code)
	}
	if totp.Validate("000000", secret, now) && code != "000000" {
		t.Fatal("unexpected TOTP accepted")
	}
	uri, err := totp.ProvisioningURI("admin@example.com", secret)
	if err != nil || !strings.HasPrefix(uri, "otpauth://totp/") {
		t.Fatalf("ProvisioningURI() = %q, %v", uri, err)
	}
}
