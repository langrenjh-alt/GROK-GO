package admin

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/langrenjh-alt/GROK-GO/internal/security"
	"github.com/langrenjh-alt/GROK-GO/internal/store"
)

var (
	ErrAlreadyBootstrapped  = errors.New("an administrator already exists")
	ErrInvalidCredentials   = errors.New("invalid administrator credentials")
	ErrLoginRateLimited     = errors.New("administrator login rate limit exceeded")
	ErrAuthStateUnavailable = errors.New("administrator authentication state is unavailable")
	ErrTOTPRequired         = errors.New("TOTP code is required")
	ErrInvalidTOTP          = errors.New("invalid TOTP code")
	ErrInvalidSession       = errors.New("invalid or expired administrator session")
	ErrInvalidCSRF          = errors.New("invalid CSRF token")
	ErrTOTPNotPending       = errors.New("TOTP enrollment is not pending or has expired")
	ErrInvalidEmail         = errors.New("valid administrator email is required")
	ErrEmailInUse           = errors.New("administrator email is already in use")
	ErrEmailUnchanged       = errors.New("new administrator email must differ from the current email")
	ErrPasswordConfirmation = errors.New("new password and confirmation do not match")
	ErrPasswordUnchanged    = errors.New("new password must differ from the current password")
	ErrPasswordTooLong      = errors.New("new password cannot exceed 4096 characters")
)

const dummyPasswordHash = "$argon2id$v=19$m=8192,t=1,p=1$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

type AuthService struct {
	repo       store.AdminRepository
	cipher     *security.Cipher
	passwords  *security.PasswordHasher
	tokens     *security.TokenManager
	totp       *security.TOTP
	state      AuthStateStore
	sessionTTL time.Duration
	now        func() time.Time
}

func NewAuthService(
	repo store.AdminRepository,
	cipher *security.Cipher,
	passwords *security.PasswordHasher,
	tokens *security.TokenManager,
	totp *security.TOTP,
	sessionTTL time.Duration,
	state ...AuthStateStore,
) *AuthService {
	service := &AuthService{
		repo: repo, cipher: cipher, passwords: passwords, tokens: tokens,
		totp: totp, sessionTTL: sessionTTL, now: time.Now,
	}
	if len(state) > 0 {
		service.state = state[0]
	}
	return service
}

type LoginRateLimitError struct {
	RetryAfter time.Duration
}

func (e *LoginRateLimitError) Error() string { return ErrLoginRateLimited.Error() }
func (e *LoginRateLimitError) Unwrap() error { return ErrLoginRateLimited }

type Principal struct {
	ID          string     `json:"id"`
	Email       string     `json:"email"`
	TOTPEnabled bool       `json:"totp_enabled"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
}

type LoginInput struct {
	Email     string
	Password  string
	TOTPCode  string
	IPAddress string
	UserAgent string
}

type LoginResult struct {
	Principal    Principal
	SessionToken string
	CSRFToken    string
	ExpiresAt    time.Time
}

type TOTPEnrollment struct {
	Secret    string
	URI       string
	ExpiresAt time.Time
}

func (s *AuthService) Bootstrap(ctx context.Context, email, password string) (*Principal, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	count, err := s.repo.CountAdmins(ctx)
	if err != nil {
		return nil, fmt.Errorf("count administrators: %w", err)
	}
	if count != 0 {
		return nil, ErrAlreadyBootstrapped
	}
	hash, err := s.passwords.Hash(password)
	if err != nil {
		return nil, err
	}
	admin := &store.AdminRecord{Email: normalizeEmail(email), PasswordHash: hash}
	if !validEmail(admin.Email) {
		return nil, ErrInvalidEmail
	}
	if err := s.repo.CreateAdmin(ctx, admin); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil, ErrAlreadyBootstrapped
		}
		return nil, fmt.Errorf("create administrator: %w", err)
	}
	principal := principalFromRecord(admin)
	return &principal, nil
}

func (s *AuthService) Login(ctx context.Context, input LoginInput) (*LoginResult, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	identity := normalizeEmail(input.Email)
	if s.state != nil {
		allowed, retryAfter, err := s.state.RegisterLoginAttempt(ctx, input.IPAddress, identity)
		if err != nil {
			return nil, authStateUnavailable("apply login rate limit", err)
		}
		if !allowed {
			return nil, &LoginRateLimitError{RetryAfter: retryAfter}
		}
	}
	if len(input.Password) > 4096 {
		return nil, ErrInvalidCredentials
	}
	admin, err := s.repo.GetAdminByEmail(ctx, identity)
	if err != nil {
		_, _ = s.passwords.Verify(input.Password, dummyPasswordHash)
		if !errors.Is(err, store.ErrNotFound) {
			return nil, fmt.Errorf("load administrator: %w", err)
		}
		return nil, ErrInvalidCredentials
	}
	validPassword, err := s.passwords.Verify(input.Password, admin.PasswordHash)
	if err != nil || !validPassword || admin.Disabled {
		return nil, ErrInvalidCredentials
	}
	if admin.TOTPEnabled {
		if strings.TrimSpace(input.TOTPCode) == "" {
			return nil, ErrTOTPRequired
		}
		secret, err := s.openTOTPSecret(admin.ID, admin.TOTPSecretCipher, false)
		if err != nil {
			return nil, fmt.Errorf("read TOTP secret: %w", err)
		}
		if !s.totp.Validate(input.TOTPCode, secret, s.now()) {
			return nil, ErrInvalidTOTP
		}
	}
	result, err := s.issueSession(ctx, admin, input.IPAddress, input.UserAgent)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	_ = s.repo.RecordAdminLogin(ctx, admin.ID, now)
	result.Principal.LastLoginAt = &now
	if s.state != nil {
		_ = s.state.ClearLoginAttempts(ctx, input.IPAddress, identity)
	}
	return result, nil
}

func (s *AuthService) AuthenticateSession(ctx context.Context, token string) (*Principal, error) {
	admin, session, err := s.loadSession(ctx, token)
	if err != nil {
		return nil, err
	}
	if s.now().Sub(session.LastSeenAt) >= 5*time.Minute {
		_ = s.repo.TouchAdminSession(ctx, session.ID, s.now().UTC())
	}
	principal := principalFromRecord(admin)
	return &principal, nil
}

func (s *AuthService) VerifyCSRF(ctx context.Context, sessionToken, csrfToken string) error {
	_, session, err := s.loadSession(ctx, sessionToken)
	if err != nil {
		return err
	}
	if !s.tokens.Verify(csrfToken, session.CSRFDigest) {
		return ErrInvalidCSRF
	}
	return nil
}

func (s *AuthService) Logout(ctx context.Context, sessionToken string) error {
	if strings.TrimSpace(sessionToken) == "" {
		return nil
	}
	digest := s.tokens.Digest(sessionToken)
	if s.state != nil {
		if err := s.state.RevokeSession(ctx, digest); err != nil {
			return authStateUnavailable("revoke administrator session", err)
		}
	}
	if err := s.repo.DeleteAdminSessionByDigest(ctx, digest); err != nil {
		return fmt.Errorf("delete administrator session: %w", err)
	}
	return nil
}

func (s *AuthService) BeginTOTP(ctx context.Context, adminID string) (*TOTPEnrollment, error) {
	admin, err := s.repo.GetAdminByID(ctx, adminID)
	if err != nil {
		return nil, err
	}
	if admin.Disabled {
		return nil, ErrInvalidCredentials
	}
	secret, err := s.totp.GenerateSecret()
	if err != nil {
		return nil, err
	}
	uri, err := s.totp.ProvisioningURI(admin.Email, secret)
	if err != nil {
		return nil, err
	}
	sealed, err := s.cipher.Seal([]byte(secret), totpAAD(admin.ID, true))
	if err != nil {
		return nil, err
	}
	expiresAt := s.now().UTC().Add(10 * time.Minute)
	if err := s.repo.SetPendingTOTP(ctx, admin.ID, sealed, expiresAt); err != nil {
		return nil, fmt.Errorf("save pending TOTP: %w", err)
	}
	return &TOTPEnrollment{Secret: secret, URI: uri, ExpiresAt: expiresAt}, nil
}

func (s *AuthService) ConfirmTOTP(ctx context.Context, adminID, code string) error {
	admin, err := s.repo.GetAdminByID(ctx, adminID)
	if err != nil {
		return err
	}
	if len(admin.PendingTOTPSecretCipher) == 0 || admin.PendingTOTPExpiresAt == nil || !admin.PendingTOTPExpiresAt.After(s.now()) {
		return ErrTOTPNotPending
	}
	secret, err := s.openTOTPSecret(admin.ID, admin.PendingTOTPSecretCipher, true)
	if err != nil {
		return ErrTOTPNotPending
	}
	if !s.totp.Validate(code, secret, s.now()) {
		return ErrInvalidTOTP
	}
	sealed, err := s.cipher.Seal([]byte(secret), totpAAD(admin.ID, false))
	if err != nil {
		return err
	}
	if err := s.repo.EnableTOTP(ctx, admin.ID, sealed); err != nil {
		return fmt.Errorf("enable TOTP: %w", err)
	}
	return nil
}

func (s *AuthService) DisableTOTP(ctx context.Context, adminID, password, code string) error {
	admin, err := s.repo.GetAdminByID(ctx, adminID)
	if err != nil {
		return err
	}
	valid, verifyErr := s.passwords.Verify(password, admin.PasswordHash)
	if verifyErr != nil || !valid {
		return ErrInvalidCredentials
	}
	if admin.TOTPEnabled {
		secret, err := s.openTOTPSecret(admin.ID, admin.TOTPSecretCipher, false)
		if err != nil || !s.totp.Validate(code, secret, s.now()) {
			return ErrInvalidTOTP
		}
	}
	if err := s.revokeAdminSessions(ctx, admin.ID); err != nil {
		return err
	}
	if err := s.repo.DisableTOTP(ctx, admin.ID); err != nil {
		return err
	}
	return s.repo.DeleteAdminSessionsForAdmin(ctx, admin.ID)
}

func (s *AuthService) ChangeEmail(ctx context.Context, adminID, email, currentPassword, totpCode string) (*Principal, error) {
	admin, err := s.repo.GetAdminByID(ctx, adminID)
	if err != nil {
		return nil, err
	}
	normalized := normalizeEmail(email)
	if !validEmail(normalized) {
		return nil, ErrInvalidEmail
	}
	if normalized == admin.Email {
		return nil, ErrEmailUnchanged
	}
	if err := s.verifyAdminCredentials(admin, currentPassword, totpCode); err != nil {
		return nil, err
	}
	existing, err := s.repo.GetAdminByEmail(ctx, normalized)
	if err == nil && existing.ID != admin.ID {
		return nil, ErrEmailInUse
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("check administrator email: %w", err)
	}
	if err := s.revokeAdminSessions(ctx, admin.ID); err != nil {
		return nil, err
	}
	if err := s.repo.UpdateAdminEmail(ctx, admin.ID, normalized); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil, ErrEmailInUse
		}
		return nil, err
	}
	if err := s.repo.DeleteAdminSessionsForAdmin(ctx, admin.ID); err != nil {
		return nil, err
	}
	admin.Email = normalized
	principal := principalFromRecord(admin)
	return &principal, nil
}

func (s *AuthService) ChangePassword(ctx context.Context, adminID, currentPassword, newPassword, confirmation, totpCode string) error {
	admin, err := s.repo.GetAdminByID(ctx, adminID)
	if err != nil {
		return err
	}
	if err := s.verifyAdminCredentials(admin, currentPassword, totpCode); err != nil {
		return err
	}
	if newPassword != confirmation {
		return ErrPasswordConfirmation
	}
	if len(newPassword) > 4096 {
		return ErrPasswordTooLong
	}
	if newPassword == currentPassword {
		return ErrPasswordUnchanged
	}
	hash, err := s.passwords.Hash(newPassword)
	if err != nil {
		return err
	}
	if err := s.revokeAdminSessions(ctx, admin.ID); err != nil {
		return err
	}
	if err := s.repo.UpdateAdminPassword(ctx, admin.ID, hash); err != nil {
		return err
	}
	return s.repo.DeleteAdminSessionsForAdmin(ctx, admin.ID)
}

// VerifyCredentials performs step-up authentication for sensitive administrator
// operations without issuing or revoking a session.
func (s *AuthService) VerifyCredentials(ctx context.Context, adminID, password, totpCode string) error {
	admin, err := s.repo.GetAdminByID(ctx, strings.TrimSpace(adminID))
	if err != nil {
		return err
	}
	return s.verifyAdminCredentials(admin, password, totpCode)
}

func (s *AuthService) verifyAdminCredentials(admin *store.AdminRecord, password, totpCode string) error {
	if len(password) > 4096 {
		return ErrInvalidCredentials
	}
	valid, verifyErr := s.passwords.Verify(password, admin.PasswordHash)
	if verifyErr != nil || !valid || admin.Disabled {
		return ErrInvalidCredentials
	}
	if admin.TOTPEnabled {
		secret, err := s.openTOTPSecret(admin.ID, admin.TOTPSecretCipher, false)
		if err != nil || !s.totp.Validate(totpCode, secret, s.now()) {
			return ErrInvalidTOTP
		}
	}
	return nil
}

func (s *AuthService) CleanupExpiredSessions(ctx context.Context) (int64, error) {
	return s.repo.DeleteExpiredAdminSessions(ctx, s.now().UTC())
}

func (s *AuthService) issueSession(ctx context.Context, admin *store.AdminRecord, ipAddress, userAgent string) (*LoginResult, error) {
	sessionToken, err := s.tokens.Generate("adm_", 32)
	if err != nil {
		return nil, err
	}
	csrfToken, err := s.tokens.Generate("csrf_", 32)
	if err != nil {
		return nil, err
	}
	expiresAt := s.now().UTC().Add(s.sessionTTL)
	revision := SessionRevision{}
	if s.state != nil {
		var err error
		revision, err = s.state.CurrentSessionRevision(ctx, admin.ID)
		if err != nil {
			return nil, authStateUnavailable("load administrator session revision", err)
		}
	}
	session := &store.AdminSession{
		AdminID: admin.ID, TokenDigest: sessionToken.Digest, CSRFDigest: csrfToken.Digest,
		IPAddress: truncate(ipAddress, 128), UserAgent: truncate(userAgent, 512), ExpiresAt: expiresAt,
	}
	if err := s.repo.CreateAdminSession(ctx, session); err != nil {
		return nil, fmt.Errorf("create administrator session: %w", err)
	}
	if s.state != nil {
		if err := s.state.SaveSession(ctx, session, revision); err != nil {
			_ = s.repo.DeleteAdminSessionByDigest(ctx, session.TokenDigest)
			return nil, authStateUnavailable("cache administrator session", err)
		}
	}
	return &LoginResult{
		Principal: principalFromRecord(admin), SessionToken: sessionToken.Plaintext,
		CSRFToken: csrfToken.Plaintext, ExpiresAt: expiresAt,
	}, nil
}

func (s *AuthService) loadSession(ctx context.Context, token string) (*store.AdminRecord, *store.AdminSession, error) {
	if strings.TrimSpace(token) == "" {
		return nil, nil, ErrInvalidSession
	}
	digest := s.tokens.Digest(token)
	var session *store.AdminSession
	if s.state != nil {
		// Only an explicit cache miss may use the PostgreSQL fallback. Redis
		// errors fail closed so an infrastructure outage cannot bypass revocation.
		cached, err := s.state.LoadSession(ctx, digest)
		switch {
		case err == nil:
			revision, revisionErr := s.state.CurrentSessionRevision(ctx, cached.Session.AdminID)
			if revisionErr != nil {
				return nil, nil, authStateUnavailable("load administrator session revision", revisionErr)
			}
			if cached.Revision.Value == revision.Value {
				session = cached.Session
			} else {
				session, err = s.loadDatabaseSession(ctx, digest, revision)
				if err != nil {
					return nil, nil, err
				}
			}
		case errors.Is(err, ErrSessionRevoked):
			return nil, nil, ErrInvalidSession
		case errors.Is(err, ErrSessionCacheMiss):
			session, err = s.loadDatabaseSessionWithRevision(ctx, digest)
			if err != nil {
				return nil, nil, err
			}
		default:
			return nil, nil, authStateUnavailable("load administrator session", err)
		}
	} else {
		var err error
		session, err = s.repo.GetAdminSessionByDigest(ctx, digest)
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				return nil, nil, fmt.Errorf("load administrator session: %w", err)
			}
			return nil, nil, ErrInvalidSession
		}
	}
	if !session.ExpiresAt.After(s.now()) {
		if s.state != nil {
			_ = s.state.RevokeSession(ctx, digest)
		}
		_ = s.repo.DeleteAdminSessionByDigest(ctx, digest)
		return nil, nil, ErrInvalidSession
	}
	admin, err := s.repo.GetAdminByID(ctx, session.AdminID)
	if err != nil || admin.Disabled {
		return nil, nil, ErrInvalidSession
	}
	return admin, session, nil
}

func (s *AuthService) loadDatabaseSessionWithRevision(ctx context.Context, digest []byte) (*store.AdminSession, error) {
	session, err := s.repo.GetAdminSessionByDigest(ctx, digest)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			return nil, fmt.Errorf("load administrator session fallback: %w", err)
		}
		return nil, ErrInvalidSession
	}
	revision, err := s.state.CurrentSessionRevision(ctx, session.AdminID)
	if err != nil {
		return nil, authStateUnavailable("load administrator session revision", err)
	}
	return s.restoreDatabaseSession(ctx, session, revision)
}

func (s *AuthService) loadDatabaseSession(ctx context.Context, digest []byte, revision SessionRevision) (*store.AdminSession, error) {
	session, err := s.repo.GetAdminSessionByDigest(ctx, digest)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			return nil, fmt.Errorf("load administrator session fallback: %w", err)
		}
		return nil, ErrInvalidSession
	}
	return s.restoreDatabaseSession(ctx, session, revision)
}

func (s *AuthService) restoreDatabaseSession(ctx context.Context, session *store.AdminSession, revision SessionRevision) (*store.AdminSession, error) {
	if !revision.NotBefore.IsZero() && !session.CreatedAt.After(revision.NotBefore) {
		_ = s.repo.DeleteAdminSessionByDigest(ctx, session.TokenDigest)
		return nil, ErrInvalidSession
	}
	if !session.ExpiresAt.After(s.now()) {
		_ = s.repo.DeleteAdminSessionByDigest(ctx, session.TokenDigest)
		return nil, ErrInvalidSession
	}
	if err := s.state.SaveSession(ctx, session, revision); err != nil {
		return nil, authStateUnavailable("restore administrator session cache", err)
	}
	return session, nil
}

func (s *AuthService) revokeAdminSessions(ctx context.Context, adminID string) error {
	if s.state != nil {
		if err := s.state.RotateSessionRevision(ctx, adminID, s.now().UTC()); err != nil {
			return authStateUnavailable("revoke administrator sessions", err)
		}
	}
	return nil
}

func authStateUnavailable(operation string, err error) error {
	return fmt.Errorf("%w: %s: %v", ErrAuthStateUnavailable, operation, err)
}

func (s *AuthService) openTOTPSecret(adminID string, ciphertext []byte, pending bool) (string, error) {
	plaintext, err := s.cipher.Open(ciphertext, totpAAD(adminID, pending))
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

func (s *AuthService) ready() error {
	if s == nil || s.repo == nil || s.cipher == nil || s.passwords == nil || s.tokens == nil || s.totp == nil {
		return errors.New("administrator authentication service is not configured")
	}
	if s.sessionTTL < 5*time.Minute {
		return errors.New("administrator session TTL must be at least 5 minutes")
	}
	return nil
}

func principalFromRecord(admin *store.AdminRecord) Principal {
	return Principal{ID: admin.ID, Email: admin.Email, TOTPEnabled: admin.TOTPEnabled, LastLoginAt: admin.LastLoginAt}
}

func normalizeEmail(email string) string { return strings.ToLower(strings.TrimSpace(email)) }

func validEmail(email string) bool {
	if email == "" || len(email) > 254 || strings.ContainsAny(email, "\r\n") {
		return false
	}
	parsed, err := mail.ParseAddress(email)
	if err != nil || parsed.Name != "" || parsed.Address != email {
		return false
	}
	parts := strings.Split(email, "@")
	return len(parts) == 2 && parts[0] != "" && strings.Contains(parts[1], ".") &&
		!strings.HasPrefix(parts[1], ".") && !strings.HasSuffix(parts[1], ".")
}

func totpAAD(adminID string, pending bool) []byte {
	state := "active"
	if pending {
		state = "pending"
	}
	return []byte("admin-totp:" + state + ":" + adminID)
}

func truncate(value string, maximum int) string {
	value = strings.ToValidUTF8(strings.TrimSpace(value), "")
	runes := []rune(value)
	if len(runes) <= maximum {
		return value
	}
	return string(runes[:maximum])
}
