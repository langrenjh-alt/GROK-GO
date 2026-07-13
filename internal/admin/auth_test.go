package admin

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/langrenjh-alt/GROK-GO/internal/security"
	"github.com/langrenjh-alt/GROK-GO/internal/store"
)

func TestAuthBootstrapLoginSessionAndCSRF(t *testing.T) {
	service, repo := newTestAuthService(t)
	ctx := context.Background()
	principal, err := service.Bootstrap(ctx, "Admin@example.com", "correct horse battery staple")
	if err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	if principal.Email != "admin@example.com" {
		t.Fatalf("principal = %+v", principal)
	}
	if _, err := service.Bootstrap(ctx, "other@example.com", "correct horse battery staple"); !errors.Is(err, ErrAlreadyBootstrapped) {
		t.Fatalf("second Bootstrap() error = %v", err)
	}

	result, err := service.Login(ctx, LoginInput{
		Email: "ADMIN@example.com", Password: "correct horse battery staple",
		IPAddress: "127.0.0.1", UserAgent: "test",
	})
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if result.SessionToken == "" || result.CSRFToken == "" || len(repo.sessions) != 1 {
		t.Fatalf("invalid login result: %+v", result)
	}
	if _, err := service.AuthenticateSession(ctx, result.SessionToken); err != nil {
		t.Fatalf("AuthenticateSession() error = %v", err)
	}
	if err := service.VerifyCSRF(ctx, result.SessionToken, result.CSRFToken); err != nil {
		t.Fatalf("VerifyCSRF() error = %v", err)
	}
	if err := service.VerifyCSRF(ctx, result.SessionToken, "csrf_wrong"); !errors.Is(err, ErrInvalidCSRF) {
		t.Fatalf("VerifyCSRF(wrong) error = %v", err)
	}
	if err := service.Logout(ctx, result.SessionToken); err != nil {
		t.Fatalf("Logout() error = %v", err)
	}
	if _, err := service.AuthenticateSession(ctx, result.SessionToken); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("AuthenticateSession(after logout) error = %v", err)
	}
}

func TestAuthTOTPEnrollmentAndLogin(t *testing.T) {
	service, _ := newTestAuthService(t)
	ctx := context.Background()
	principal, err := service.Bootstrap(ctx, "admin@example.com", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := service.BeginTOTP(ctx, principal.ID)
	if err != nil {
		t.Fatalf("BeginTOTP() error = %v", err)
	}
	code := testTOTPCode(t, enrollment.Secret, service.now())
	if err := service.ConfirmTOTP(ctx, principal.ID, code); err != nil {
		t.Fatalf("ConfirmTOTP() error = %v", err)
	}
	if _, err := service.Login(ctx, LoginInput{Email: principal.Email, Password: "correct horse battery staple"}); !errors.Is(err, ErrTOTPRequired) {
		t.Fatalf("Login(without TOTP) error = %v", err)
	}
	result, err := service.Login(ctx, LoginInput{Email: principal.Email, Password: "correct horse battery staple", TOTPCode: code})
	if err != nil || !result.Principal.TOTPEnabled {
		t.Fatalf("Login(with TOTP) = %+v, %v", result, err)
	}
}

func TestAuthCredentialChangesValidateAndRevokeSessions(t *testing.T) {
	service, repo := newTestAuthService(t)
	ctx := context.Background()
	principal, err := service.Bootstrap(ctx, "admin@example.com", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	login, err := service.Login(ctx, LoginInput{Email: principal.Email, Password: "correct horse battery staple"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ChangeEmail(ctx, principal.ID, "not-an-email", "correct horse battery staple", ""); !errors.Is(err, ErrInvalidEmail) {
		t.Fatalf("ChangeEmail(invalid) error = %v", err)
	}
	repo.admins["admin-2"] = &store.AdminRecord{ID: "admin-2", Email: "existing@example.com", PasswordHash: repo.admins[principal.ID].PasswordHash}
	if _, err := service.ChangeEmail(ctx, principal.ID, "EXISTING@example.com", "correct horse battery staple", ""); !errors.Is(err, ErrEmailInUse) {
		t.Fatalf("ChangeEmail(existing) error = %v", err)
	}
	updated, err := service.ChangeEmail(ctx, principal.ID, " New.Admin@Example.COM ", "correct horse battery staple", "")
	if err != nil {
		t.Fatalf("ChangeEmail() error = %v", err)
	}
	if updated.Email != "new.admin@example.com" || repo.admins[principal.ID].Email != updated.Email {
		t.Fatalf("updated principal = %+v", updated)
	}
	if _, err := service.AuthenticateSession(ctx, login.SessionToken); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("old session after email change error = %v", err)
	}

	login, err = service.Login(ctx, LoginInput{Email: updated.Email, Password: "correct horse battery staple"})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ChangePassword(ctx, principal.ID, "correct horse battery staple", "new correct horse battery staple", "mismatch", ""); !errors.Is(err, ErrPasswordConfirmation) {
		t.Fatalf("ChangePassword(mismatch) error = %v", err)
	}
	if err := service.ChangePassword(ctx, principal.ID, "correct horse battery staple", "new correct horse battery staple", "new correct horse battery staple", ""); err != nil {
		t.Fatalf("ChangePassword() error = %v", err)
	}
	if _, err := service.AuthenticateSession(ctx, login.SessionToken); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("old session after password change error = %v", err)
	}
	if _, err := service.Login(ctx, LoginInput{Email: updated.Email, Password: "new correct horse battery staple"}); err != nil {
		t.Fatalf("Login(new password) error = %v", err)
	}
}

func newTestAuthService(t *testing.T) (*AuthService, *fakeAdminRepo) {
	t.Helper()
	repo := &fakeAdminRepo{admins: map[string]*store.AdminRecord{}, sessions: map[string]*store.AdminSession{}}
	cipher, _ := security.NewCipher([]byte("0123456789abcdef0123456789abcdef"))
	hasher, err := security.NewPasswordHasher(security.Argon2Params{Memory: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32})
	if err != nil {
		t.Fatal(err)
	}
	tokens, _ := security.NewTokenManager([]byte("abcdef0123456789abcdef0123456789"))
	totp, _ := security.NewTOTP("GROK-GO")
	service := NewAuthService(repo, cipher, hasher, tokens, totp, time.Hour)
	service.now = func() time.Time { return time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC) }
	return service, repo
}

type fakeAdminRepo struct {
	mu       sync.Mutex
	admins   map[string]*store.AdminRecord
	sessions map[string]*store.AdminSession
}

func (r *fakeAdminRepo) CountAdmins(context.Context) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return int64(len(r.admins)), nil
}
func (r *fakeAdminRepo) CreateAdmin(_ context.Context, admin *store.AdminRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.admins {
		if existing.Email == admin.Email {
			return store.ErrConflict
		}
	}
	if admin.ID == "" {
		admin.ID = fmt.Sprintf("admin-%d", len(r.admins)+1)
	}
	now := time.Now().UTC()
	admin.CreatedAt, admin.UpdatedAt = now, now
	copy := *admin
	r.admins[admin.ID] = &copy
	return nil
}
func (r *fakeAdminRepo) GetAdminByID(_ context.Context, id string) (*store.AdminRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.admins[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	copy := *value
	return &copy, nil
}
func (r *fakeAdminRepo) GetAdminByEmail(_ context.Context, email string) (*store.AdminRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, value := range r.admins {
		if value.Email == normalizeEmail(email) {
			copy := *value
			return &copy, nil
		}
	}
	return nil, store.ErrNotFound
}
func (r *fakeAdminRepo) UpdateAdminEmail(_ context.Context, id, email string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.admins[id]
	if !ok {
		return store.ErrNotFound
	}
	email = normalizeEmail(email)
	for otherID, other := range r.admins {
		if otherID != id && other.Email == email {
			return store.ErrConflict
		}
	}
	value.Email = email
	return nil
}
func (r *fakeAdminRepo) UpdateAdminPassword(_ context.Context, id, hash string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.admins[id]
	if !ok {
		return store.ErrNotFound
	}
	value.PasswordHash = hash
	return nil
}
func (r *fakeAdminRepo) SetPendingTOTP(_ context.Context, id string, cipher []byte, expires time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.admins[id]
	if !ok {
		return store.ErrNotFound
	}
	value.PendingTOTPSecretCipher = append([]byte(nil), cipher...)
	value.PendingTOTPExpiresAt = &expires
	return nil
}
func (r *fakeAdminRepo) EnableTOTP(_ context.Context, id string, cipher []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.admins[id]
	if !ok {
		return store.ErrNotFound
	}
	value.TOTPSecretCipher = append([]byte(nil), cipher...)
	value.TOTPEnabled = true
	value.PendingTOTPSecretCipher = nil
	value.PendingTOTPExpiresAt = nil
	return nil
}
func (r *fakeAdminRepo) DisableTOTP(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.admins[id]
	if !ok {
		return store.ErrNotFound
	}
	value.TOTPEnabled = false
	value.TOTPSecretCipher = nil
	return nil
}
func (r *fakeAdminRepo) RecordAdminLogin(_ context.Context, id string, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.admins[id]
	if !ok {
		return store.ErrNotFound
	}
	value.LastLoginAt = &at
	return nil
}
func (r *fakeAdminRepo) CreateAdminSession(_ context.Context, session *store.AdminSession) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	session.ID = fmt.Sprintf("session-%d", len(r.sessions)+1)
	session.CreatedAt = time.Now()
	session.LastSeenAt = session.CreatedAt
	copy := *session
	r.sessions[string(session.TokenDigest)] = &copy
	return nil
}
func (r *fakeAdminRepo) GetAdminSessionByDigest(_ context.Context, digest []byte) (*store.AdminSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.sessions[string(digest)]
	if !ok {
		return nil, store.ErrNotFound
	}
	copy := *value
	return &copy, nil
}
func (r *fakeAdminRepo) TouchAdminSession(_ context.Context, id string, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, value := range r.sessions {
		if value.ID == id {
			value.LastSeenAt = at
			return nil
		}
	}
	return store.ErrNotFound
}
func (r *fakeAdminRepo) DeleteAdminSessionByDigest(_ context.Context, digest []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.sessions, string(digest))
	return nil
}
func (r *fakeAdminRepo) DeleteAdminSessionsForAdmin(_ context.Context, adminID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, value := range r.sessions {
		if value.AdminID == adminID {
			delete(r.sessions, key)
		}
	}
	return nil
}
func (r *fakeAdminRepo) DeleteExpiredAdminSessions(_ context.Context, before time.Time) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var count int64
	for key, value := range r.sessions {
		if !value.ExpiresAt.After(before) {
			delete(r.sessions, key)
			count++
		}
	}
	return count, nil
}

func testTOTPCode(t *testing.T, secret string, now time.Time) string {
	t.Helper()
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	if err != nil {
		t.Fatal(err)
	}
	var payload [8]byte
	binary.BigEndian.PutUint64(payload[:], uint64(now.Unix()/30))
	mac := hmac.New(sha1.New, key)
	_, _ = mac.Write(payload[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := (uint32(sum[offset])&0x7f)<<24 | uint32(sum[offset+1])<<16 | uint32(sum[offset+2])<<8 | uint32(sum[offset+3])
	return fmt.Sprintf("%06d", value%1_000_000)
}
