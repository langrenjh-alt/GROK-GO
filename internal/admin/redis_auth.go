package admin

import (
	"context"
	"crypto/hmac"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/langrenjh-alt/GROK-GO/internal/database"
	"github.com/langrenjh-alt/GROK-GO/internal/security"
	"github.com/langrenjh-alt/GROK-GO/internal/store"
)

var (
	ErrSessionCacheMiss = errors.New("administrator session is not cached")
	ErrSessionRevoked   = errors.New("administrator session has been revoked")
)

type SessionRevision struct {
	Value     string    `json:"value"`
	NotBefore time.Time `json:"not_before,omitempty"`
}

type CachedSession struct {
	Session  *store.AdminSession `json:"session"`
	Revision SessionRevision     `json:"revision"`
}

type AuthStateStore interface {
	LoadSession(context.Context, []byte) (*CachedSession, error)
	SaveSession(context.Context, *store.AdminSession, SessionRevision) error
	RevokeSession(context.Context, []byte) error
	CurrentSessionRevision(context.Context, string) (SessionRevision, error)
	RotateSessionRevision(context.Context, string, time.Time) error
	RegisterLoginAttempt(context.Context, string, string) (bool, time.Duration, error)
	ClearLoginAttempts(context.Context, string, string) error
}

type LoginRateLimit struct {
	MaxAttempts int64
	Window      time.Duration
}

func DefaultLoginRateLimit() LoginRateLimit {
	return LoginRateLimit{MaxAttempts: 5, Window: 5 * time.Minute}
}

type redisAuthClient interface {
	Get(context.Context, string) ([]byte, error)
	Set(context.Context, string, []byte, time.Duration) error
	SetNX(context.Context, string, []byte, time.Duration) (bool, error)
	Delete(context.Context, ...string) error
	IncrementWindow(context.Context, string, time.Duration) (int64, error)
}

type RedisAuthState struct {
	redis      redisAuthClient
	tokens     *security.TokenManager
	sessionTTL time.Duration
	limit      LoginRateLimit
	now        func() time.Time
}

func NewRedisAuthState(redis *database.Redis, tokens *security.TokenManager, sessionTTL time.Duration, limits ...LoginRateLimit) *RedisAuthState {
	return newRedisAuthState(redis, tokens, sessionTTL, limits...)
}

func newRedisAuthState(redis redisAuthClient, tokens *security.TokenManager, sessionTTL time.Duration, limits ...LoginRateLimit) *RedisAuthState {
	limit := DefaultLoginRateLimit()
	if len(limits) > 0 {
		limit = limits[0]
	}
	return &RedisAuthState{redis: redis, tokens: tokens, sessionTTL: sessionTTL, limit: limit, now: time.Now}
}

func (s *RedisAuthState) LoadSession(ctx context.Context, digest []byte) (*CachedSession, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	if len(digest) == 0 {
		return nil, ErrSessionCacheMiss
	}
	if _, err := s.redis.Get(ctx, s.revokedSessionKey(digest)); err == nil {
		return nil, ErrSessionRevoked
	} else if !errors.Is(err, database.ErrCacheMiss) {
		return nil, err
	}
	encoded, err := s.redis.Get(ctx, s.sessionKey(digest))
	if errors.Is(err, database.ErrCacheMiss) {
		return nil, ErrSessionCacheMiss
	}
	if err != nil {
		return nil, err
	}
	var cached CachedSession
	if err := json.Unmarshal(encoded, &cached); err != nil {
		return nil, fmt.Errorf("decode cached administrator session: %w", err)
	}
	if cached.Session == nil || cached.Session.ID == "" || cached.Session.AdminID == "" || len(cached.Session.CSRFDigest) == 0 || cached.Revision.Value == "" {
		return nil, errors.New("cached administrator session is incomplete")
	}
	if len(cached.Session.TokenDigest) != 0 && !hmac.Equal(cached.Session.TokenDigest, digest) {
		return nil, errors.New("cached administrator session digest does not match")
	}
	cached.Session.TokenDigest = append([]byte(nil), digest...)
	return &cached, nil
}

func (s *RedisAuthState) SaveSession(ctx context.Context, session *store.AdminSession, revision SessionRevision) error {
	if err := s.ready(); err != nil {
		return err
	}
	if session == nil || len(session.TokenDigest) == 0 || session.AdminID == "" || revision.Value == "" {
		return errors.New("administrator session and revision are required")
	}
	ttl := session.ExpiresAt.Sub(s.now().UTC())
	if ttl <= 0 {
		return errors.New("administrator session has already expired")
	}
	encoded, err := json.Marshal(CachedSession{Session: session, Revision: revision})
	if err != nil {
		return fmt.Errorf("encode administrator session: %w", err)
	}
	return s.redis.Set(ctx, s.sessionKey(session.TokenDigest), encoded, ttl)
}

func (s *RedisAuthState) RevokeSession(ctx context.Context, digest []byte) error {
	if err := s.ready(); err != nil {
		return err
	}
	if len(digest) == 0 {
		return nil
	}
	if err := s.redis.Set(ctx, s.revokedSessionKey(digest), []byte("1"), s.sessionTTL); err != nil {
		return err
	}
	// The tombstone is authoritative; deletion only reclaims the cached payload early.
	_ = s.redis.Delete(ctx, s.sessionKey(digest))
	return nil
}

func (s *RedisAuthState) CurrentSessionRevision(ctx context.Context, adminID string) (SessionRevision, error) {
	if err := s.ready(); err != nil {
		return SessionRevision{}, err
	}
	key := s.revisionKey(adminID)
	encoded, err := s.redis.Get(ctx, key)
	if err == nil {
		revision, decodeErr := decodeSessionRevision(encoded)
		if decodeErr != nil {
			return SessionRevision{}, decodeErr
		}
		return revision, nil
	}
	if !errors.Is(err, database.ErrCacheMiss) {
		return SessionRevision{}, err
	}
	revision, encoded, err := s.newRevision(time.Time{})
	if err != nil {
		return SessionRevision{}, err
	}
	created, err := s.redis.SetNX(ctx, key, encoded, 0)
	if err != nil {
		return SessionRevision{}, err
	}
	if created {
		return revision, nil
	}
	encoded, err = s.redis.Get(ctx, key)
	if err != nil {
		return SessionRevision{}, err
	}
	return decodeSessionRevision(encoded)
}

func (s *RedisAuthState) RotateSessionRevision(ctx context.Context, adminID string, notBefore time.Time) error {
	if err := s.ready(); err != nil {
		return err
	}
	_, encoded, err := s.newRevision(notBefore.UTC())
	if err != nil {
		return err
	}
	return s.redis.Set(ctx, s.revisionKey(adminID), encoded, 0)
}

func (s *RedisAuthState) RegisterLoginAttempt(ctx context.Context, ipAddress, identity string) (bool, time.Duration, error) {
	if err := s.ready(); err != nil {
		return false, 0, err
	}
	if s.limit.MaxAttempts <= 0 || s.limit.Window <= 0 {
		return false, 0, errors.New("administrator login rate limit is invalid")
	}
	count, err := s.redis.IncrementWindow(ctx, s.loginKey(ipAddress, identity), s.limit.Window)
	if err != nil {
		return false, 0, err
	}
	if count > s.limit.MaxAttempts {
		return false, s.limit.Window, nil
	}
	return true, 0, nil
}

func (s *RedisAuthState) ClearLoginAttempts(ctx context.Context, ipAddress, identity string) error {
	if err := s.ready(); err != nil {
		return err
	}
	return s.redis.Delete(ctx, s.loginKey(ipAddress, identity))
}

func (s *RedisAuthState) newRevision(notBefore time.Time) (SessionRevision, []byte, error) {
	material, err := s.tokens.Generate("rev_", 24)
	if err != nil {
		return SessionRevision{}, nil, err
	}
	revision := SessionRevision{Value: material.Plaintext, NotBefore: notBefore}
	encoded, err := json.Marshal(revision)
	return revision, encoded, err
}

func decodeSessionRevision(encoded []byte) (SessionRevision, error) {
	var revision SessionRevision
	if err := json.Unmarshal(encoded, &revision); err != nil {
		return SessionRevision{}, fmt.Errorf("decode administrator session revision: %w", err)
	}
	if revision.Value == "" {
		return SessionRevision{}, errors.New("administrator session revision is empty")
	}
	return revision, nil
}

func (s *RedisAuthState) sessionKey(digest []byte) string {
	return "admin:session:" + hex.EncodeToString(digest)
}

func (s *RedisAuthState) revokedSessionKey(digest []byte) string {
	return "admin:session-revoked:" + hex.EncodeToString(digest)
}

func (s *RedisAuthState) revisionKey(adminID string) string {
	digest := s.tokens.Digest("admin-session-revision\x00" + strings.TrimSpace(adminID))
	return "admin:session-revision:" + hex.EncodeToString(digest)
}

func (s *RedisAuthState) loginKey(ipAddress, identity string) string {
	identity = normalizeEmail(identity)
	ipAddress = strings.ToLower(strings.TrimSpace(ipAddress))
	digest := s.tokens.Digest("admin-login\x00" + ipAddress + "\x00" + identity)
	return "admin:login:" + hex.EncodeToString(digest)
}

func (s *RedisAuthState) ready() error {
	if s == nil || s.redis == nil || s.tokens == nil {
		return errors.New("Redis administrator authentication state is not configured")
	}
	if s.sessionTTL < 5*time.Minute {
		return errors.New("Redis administrator session TTL must be at least 5 minutes")
	}
	return nil
}

var _ AuthStateStore = (*RedisAuthState)(nil)
