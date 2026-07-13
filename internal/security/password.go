package security

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

var ErrInvalidPasswordHash = errors.New("invalid password hash")

type Argon2Params struct {
	Memory      uint32
	Iterations  uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
}

func DefaultArgon2Params() Argon2Params {
	parallelism := runtime.NumCPU()
	if parallelism > 4 {
		parallelism = 4
	}
	if parallelism < 1 {
		parallelism = 1
	}
	return Argon2Params{
		Memory:      64 * 1024,
		Iterations:  3,
		Parallelism: uint8(parallelism),
		SaltLength:  16,
		KeyLength:   32,
	}
}

type PasswordHasher struct {
	params Argon2Params
	rand   io.Reader
}

func NewPasswordHasher(params Argon2Params) (*PasswordHasher, error) {
	if err := validateArgon2Params(params); err != nil {
		return nil, err
	}
	return &PasswordHasher{params: params, rand: rand.Reader}, nil
}

func NewDefaultPasswordHasher() *PasswordHasher {
	hasher, err := NewPasswordHasher(DefaultArgon2Params())
	if err != nil {
		panic(err)
	}
	return hasher
}

func (h *PasswordHasher) Hash(password string) (string, error) {
	if h == nil {
		return "", errors.New("password hasher is not configured")
	}
	if len(password) < 12 {
		return "", errors.New("password must contain at least 12 characters")
	}
	salt := make([]byte, h.params.SaltLength)
	if _, err := io.ReadFull(h.rand, salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}
	hash := argon2.IDKey([]byte(password), salt, h.params.Iterations, h.params.Memory, h.params.Parallelism, h.params.KeyLength)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		h.params.Memory,
		h.params.Iterations,
		h.params.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	), nil
}

func (h *PasswordHasher) Verify(password, encoded string) (bool, error) {
	params, salt, expected, err := parseArgon2Hash(encoded)
	if err != nil {
		return false, err
	}
	actual := argon2.IDKey([]byte(password), salt, params.Iterations, params.Memory, params.Parallelism, uint32(len(expected)))
	return subtle.ConstantTimeCompare(actual, expected) == 1, nil
}

func validateArgon2Params(params Argon2Params) error {
	switch {
	case params.Memory < 8*1024 || params.Memory > 1024*1024:
		return errors.New("Argon2 memory must be between 8192 KiB and 1048576 KiB")
	case params.Iterations < 1 || params.Iterations > 10:
		return errors.New("Argon2 iterations must be between 1 and 10")
	case params.Parallelism < 1 || params.Parallelism > 16:
		return errors.New("Argon2 parallelism must be between 1 and 16")
	case params.SaltLength < 16 || params.SaltLength > 64:
		return errors.New("Argon2 salt length must be between 16 and 64")
	case params.KeyLength < 16 || params.KeyLength > 64:
		return errors.New("Argon2 key length must be between 16 and 64")
	default:
		return nil
	}
}

func parseArgon2Hash(encoded string) (Argon2Params, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return Argon2Params{}, nil, nil, ErrInvalidPasswordHash
	}
	var params Argon2Params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &params.Memory, &params.Iterations, &params.Parallelism); err != nil {
		return Argon2Params{}, nil, nil, ErrInvalidPasswordHash
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return Argon2Params{}, nil, nil, ErrInvalidPasswordHash
	}
	hash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return Argon2Params{}, nil, nil, ErrInvalidPasswordHash
	}
	params.SaltLength = uint32(len(salt))
	params.KeyLength = uint32(len(hash))
	if err := validateArgon2Params(params); err != nil {
		return Argon2Params{}, nil, nil, ErrInvalidPasswordHash
	}
	return params, salt, hash, nil
}
