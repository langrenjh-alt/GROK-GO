package admin

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/langrenjh-alt/GROK-GO/internal/database"
	"github.com/langrenjh-alt/GROK-GO/internal/security"
	"github.com/langrenjh-alt/GROK-GO/internal/store"
)

func TestRedisAuthStateStoresRevokesAndHashesKeys(t *testing.T) {
	redis := newFakeRedisAuthClient()
	tokens := testTokenManager(t)
	now := time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC)
	state := newRedisAuthState(redis, tokens, time.Hour)
	state.now = func() time.Time { return now }

	revision, err := state.CurrentSessionRevision(context.Background(), "admin-1")
	if err != nil {
		t.Fatalf("CurrentSessionRevision() error = %v", err)
	}
	digest := tokens.Digest("adm_plaintext-session-token")
	session := &store.AdminSession{
		ID: "session-1", AdminID: "admin-1", TokenDigest: digest,
		CSRFDigest: tokens.Digest("csrf_plaintext-token"), CreatedAt: now,
		LastSeenAt: now, ExpiresAt: now.Add(time.Hour),
	}
	if err := state.SaveSession(context.Background(), session, revision); err != nil {
		t.Fatalf("SaveSession() error = %v", err)
	}
	cached, err := state.LoadSession(context.Background(), digest)
	if err != nil || cached.Session.ID != session.ID || cached.Revision.Value != revision.Value {
		t.Fatalf("LoadSession() = %+v, %v", cached, err)
	}
	for key := range redis.values {
		if strings.Contains(key, "admin-1") || strings.Contains(key, "plaintext") {
			t.Fatalf("Redis key contains sensitive input: %q", key)
		}
	}
	if err := state.RevokeSession(context.Background(), digest); err != nil {
		t.Fatalf("RevokeSession() error = %v", err)
	}
	if _, err := state.LoadSession(context.Background(), digest); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("LoadSession(after revoke) error = %v", err)
	}
}

func TestRedisAuthStateRateLimitsNormalizedHashedIdentity(t *testing.T) {
	redis := newFakeRedisAuthClient()
	state := newRedisAuthState(redis, testTokenManager(t), time.Hour, LoginRateLimit{MaxAttempts: 2, Window: 90 * time.Second})
	ctx := context.Background()
	for index, identity := range []string{" Admin@Example.com ", "admin@example.com"} {
		allowed, _, err := state.RegisterLoginAttempt(ctx, "203.0.113.9", identity)
		if err != nil || !allowed {
			t.Fatalf("RegisterLoginAttempt(%d) = %v, %v", index, allowed, err)
		}
	}
	allowed, retryAfter, err := state.RegisterLoginAttempt(ctx, "203.0.113.9", "ADMIN@EXAMPLE.COM")
	if err != nil || allowed || retryAfter != 90*time.Second {
		t.Fatalf("limited attempt = %v, %v, %v", allowed, retryAfter, err)
	}
	for key := range redis.counters {
		if strings.Contains(key, "admin@example.com") || strings.Contains(key, "203.0.113.9") {
			t.Fatalf("rate-limit key contains plaintext input: %q", key)
		}
	}
	if err := state.ClearLoginAttempts(ctx, "203.0.113.9", "admin@example.com"); err != nil {
		t.Fatal(err)
	}
	allowed, _, err = state.RegisterLoginAttempt(ctx, "203.0.113.9", "admin@example.com")
	if err != nil || !allowed {
		t.Fatalf("attempt after reset = %v, %v", allowed, err)
	}
}

func TestAuthServiceRedisFallbackAndFailClosed(t *testing.T) {
	service, repo, redis, _ := newRedisTestAuthService(t, DefaultLoginRateLimit())
	ctx := context.Background()
	if _, err := service.Bootstrap(ctx, "admin@example.com", "correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	result, err := service.Login(ctx, LoginInput{Email: "admin@example.com", Password: "correct horse battery staple", IPAddress: "127.0.0.1"})
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	digest := service.tokens.Digest(result.SessionToken)
	cacheKey := service.state.(*RedisAuthState).sessionKey(digest)
	if _, ok := redis.values[cacheKey]; !ok || len(repo.sessions) != 1 {
		t.Fatalf("session was not written to Redis and PostgreSQL fixture")
	}
	delete(redis.values, cacheKey)
	if _, err := service.AuthenticateSession(ctx, result.SessionToken); err != nil {
		t.Fatalf("AuthenticateSession(DB fallback) error = %v", err)
	}
	if _, ok := redis.values[cacheKey]; !ok {
		t.Fatal("database fallback did not restore Redis session")
	}
	redis.getErr = errors.New("Redis offline")
	if _, err := service.AuthenticateSession(ctx, result.SessionToken); !errors.Is(err, ErrAuthStateUnavailable) {
		t.Fatalf("AuthenticateSession(Redis failure) error = %v", err)
	}
	redis.getErr = nil
	if err := service.Logout(ctx, result.SessionToken); err != nil {
		t.Fatalf("Logout() error = %v", err)
	}
	if _, err := service.AuthenticateSession(ctx, result.SessionToken); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("AuthenticateSession(after logout) error = %v", err)
	}
}

func TestAuthServiceRedisRateLimitAndBulkRevocation(t *testing.T) {
	service, _, _, _ := newRedisTestAuthService(t, LoginRateLimit{MaxAttempts: 2, Window: time.Minute})
	ctx := context.Background()
	principal, err := service.Bootstrap(ctx, "admin@example.com", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 2; index++ {
		if _, err := service.Login(ctx, LoginInput{Email: principal.Email, Password: "wrong", IPAddress: "192.0.2.1"}); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("failed login %d error = %v", index, err)
		}
	}
	if _, err := service.Login(ctx, LoginInput{Email: principal.Email, Password: "wrong", IPAddress: "192.0.2.1"}); !errors.Is(err, ErrLoginRateLimited) {
		t.Fatalf("limited login error = %v", err)
	}

	first, err := service.Login(ctx, LoginInput{Email: principal.Email, Password: "correct horse battery staple", IPAddress: "192.0.2.2"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Login(ctx, LoginInput{Email: principal.Email, Password: "correct horse battery staple", IPAddress: "192.0.2.3"})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ChangePassword(ctx, principal.ID, "correct horse battery staple", "new correct horse battery staple", "new correct horse battery staple", ""); err != nil {
		t.Fatalf("ChangePassword() error = %v", err)
	}
	for _, token := range []string{first.SessionToken, second.SessionToken} {
		if _, err := service.AuthenticateSession(ctx, token); !errors.Is(err, ErrInvalidSession) {
			t.Fatalf("old session after password change error = %v", err)
		}
	}
}

func TestAuthServiceTOTPDisableRevokesRedisSessions(t *testing.T) {
	service, _, _, _ := newRedisTestAuthService(t, DefaultLoginRateLimit())
	ctx := context.Background()
	principal, err := service.Bootstrap(ctx, "admin@example.com", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := service.BeginTOTP(ctx, principal.ID)
	if err != nil {
		t.Fatal(err)
	}
	code := testTOTPCode(t, enrollment.Secret, service.now())
	if err := service.ConfirmTOTP(ctx, principal.ID, code); err != nil {
		t.Fatal(err)
	}
	result, err := service.Login(ctx, LoginInput{Email: principal.Email, Password: "correct horse battery staple", TOTPCode: code, IPAddress: "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.DisableTOTP(ctx, principal.ID, "correct horse battery staple", code); err != nil {
		t.Fatalf("DisableTOTP() error = %v", err)
	}
	if _, err := service.AuthenticateSession(ctx, result.SessionToken); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("old session after TOTP disable error = %v", err)
	}
}

func newRedisTestAuthService(t *testing.T, limit LoginRateLimit) (*AuthService, *fakeAdminRepo, *fakeRedisAuthClient, *RedisAuthState) {
	t.Helper()
	service, repo := newTestAuthService(t)
	redis := newFakeRedisAuthClient()
	state := newRedisAuthState(redis, service.tokens, service.sessionTTL, limit)
	state.now = service.now
	service.state = state
	return service, repo, redis, state
}

func testTokenManager(t *testing.T) *security.TokenManager {
	t.Helper()
	tokens, err := security.NewTokenManager([]byte("abcdef0123456789abcdef0123456789"))
	if err != nil {
		t.Fatal(err)
	}
	return tokens
}

type fakeRedisAuthClient struct {
	mu       sync.Mutex
	values   map[string][]byte
	counters map[string]int64
	getErr   error
	setErr   error
}

func newFakeRedisAuthClient() *fakeRedisAuthClient {
	return &fakeRedisAuthClient{values: make(map[string][]byte), counters: make(map[string]int64)}
}

func (r *fakeRedisAuthClient) Get(_ context.Context, key string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.getErr != nil {
		return nil, r.getErr
	}
	value, ok := r.values[key]
	if !ok {
		return nil, database.ErrCacheMiss
	}
	return append([]byte(nil), value...), nil
}

func (r *fakeRedisAuthClient) Set(_ context.Context, key string, value []byte, _ time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.setErr != nil {
		return r.setErr
	}
	r.values[key] = append([]byte(nil), value...)
	return nil
}

func (r *fakeRedisAuthClient) SetNX(_ context.Context, key string, value []byte, _ time.Duration) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.setErr != nil {
		return false, r.setErr
	}
	if _, ok := r.values[key]; ok {
		return false, nil
	}
	r.values[key] = append([]byte(nil), value...)
	return true, nil
}

func (r *fakeRedisAuthClient) Delete(_ context.Context, keys ...string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.setErr != nil {
		return r.setErr
	}
	for _, key := range keys {
		delete(r.values, key)
		delete(r.counters, key)
	}
	return nil
}

func (r *fakeRedisAuthClient) IncrementWindow(_ context.Context, key string, _ time.Duration) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.setErr != nil {
		return 0, r.setErr
	}
	r.counters[key]++
	return r.counters[key], nil
}
