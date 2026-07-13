package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type AdminRecord struct {
	ID                      string
	Email                   string
	PasswordHash            string
	TOTPSecretCipher        []byte
	TOTPEnabled             bool
	PendingTOTPSecretCipher []byte
	PendingTOTPExpiresAt    *time.Time
	Disabled                bool
	LastLoginAt             *time.Time
	CreatedAt               time.Time
	UpdatedAt               time.Time
}

type AdminSession struct {
	ID          string
	AdminID     string
	TokenDigest []byte
	CSRFDigest  []byte
	IPAddress   string
	UserAgent   string
	ExpiresAt   time.Time
	LastSeenAt  time.Time
	CreatedAt   time.Time
}

const adminColumns = `
	id, email, password_hash, totp_secret_cipher, totp_enabled,
	pending_totp_secret_cipher, pending_totp_expires_at, disabled,
	last_login_at, created_at, updated_at`

const adminSessionColumns = `
	id, admin_id, token_digest, csrf_digest, ip_address, user_agent,
	expires_at, last_seen_at, created_at`

func (p *Postgres) CountAdmins(ctx context.Context) (int64, error) {
	var count int64
	if err := p.db.QueryRow(ctx, `SELECT count(*) FROM admins`).Scan(&count); err != nil {
		return 0, fmt.Errorf("count admins: %w", err)
	}
	return count, nil
}

func (p *Postgres) CreateAdmin(ctx context.Context, admin *AdminRecord) error {
	if admin == nil {
		return errorsNew("admin is required")
	}
	id, err := newID(admin.ID)
	if err != nil {
		return err
	}
	email := strings.ToLower(strings.TrimSpace(admin.Email))
	err = p.db.QueryRow(ctx, `
		INSERT INTO admins (id, email, password_hash, disabled)
		VALUES ($1, $2, $3, $4)
		RETURNING created_at, updated_at`,
		id, email, admin.PasswordHash, admin.Disabled,
	).Scan(&admin.CreatedAt, &admin.UpdatedAt)
	if err != nil {
		return translateError(err)
	}
	admin.ID = id
	admin.Email = email
	return nil
}

func (p *Postgres) GetAdminByID(ctx context.Context, id string) (*AdminRecord, error) {
	return scanAdmin(p.db.QueryRow(ctx, `SELECT `+adminColumns+` FROM admins WHERE id = $1`, id))
}

func (p *Postgres) GetAdminByEmail(ctx context.Context, email string) (*AdminRecord, error) {
	return scanAdmin(p.db.QueryRow(ctx, `SELECT `+adminColumns+` FROM admins WHERE email = lower(btrim($1))`, email))
}

func (p *Postgres) UpdateAdminEmail(ctx context.Context, id, email string) error {
	tag, err := p.db.Exec(ctx, `UPDATE admins SET email = lower(btrim($2)), updated_at = now() WHERE id = $1`, id, email)
	if err != nil {
		return translateError(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (p *Postgres) UpdateAdminPassword(ctx context.Context, id, passwordHash string) error {
	tag, err := p.db.Exec(ctx, `UPDATE admins SET password_hash = $2, updated_at = now() WHERE id = $1`, id, passwordHash)
	if err != nil {
		return translateError(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (p *Postgres) SetPendingTOTP(ctx context.Context, id string, cipher []byte, expiresAt time.Time) error {
	tag, err := p.db.Exec(ctx, `
		UPDATE admins SET pending_totp_secret_cipher = $2,
			pending_totp_expires_at = $3, updated_at = now()
		WHERE id = $1`, id, cipher, expiresAt.UTC())
	if err != nil {
		return translateError(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (p *Postgres) EnableTOTP(ctx context.Context, id string, cipher []byte) error {
	tag, err := p.db.Exec(ctx, `
		UPDATE admins SET totp_secret_cipher = $2, totp_enabled = TRUE,
			pending_totp_secret_cipher = NULL, pending_totp_expires_at = NULL,
			updated_at = now()
		WHERE id = $1`, id, cipher)
	if err != nil {
		return translateError(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (p *Postgres) DisableTOTP(ctx context.Context, id string) error {
	tag, err := p.db.Exec(ctx, `
		UPDATE admins SET totp_secret_cipher = NULL, totp_enabled = FALSE,
			pending_totp_secret_cipher = NULL, pending_totp_expires_at = NULL,
			updated_at = now()
		WHERE id = $1`, id)
	if err != nil {
		return translateError(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (p *Postgres) RecordAdminLogin(ctx context.Context, id string, at time.Time) error {
	tag, err := p.db.Exec(ctx, `UPDATE admins SET last_login_at = $2, updated_at = now() WHERE id = $1`, id, ensureTime(at))
	if err != nil {
		return translateError(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (p *Postgres) CreateAdminSession(ctx context.Context, session *AdminSession) error {
	if session == nil || len(session.TokenDigest) == 0 || len(session.CSRFDigest) == 0 {
		return errorsNew("admin session and token digests are required")
	}
	id, err := newID(session.ID)
	if err != nil {
		return err
	}
	err = p.db.QueryRow(ctx, `
		INSERT INTO admin_sessions (
			id, admin_id, token_digest, csrf_digest, ip_address, user_agent, expires_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING created_at, last_seen_at`,
		id, session.AdminID, session.TokenDigest, session.CSRFDigest,
		session.IPAddress, session.UserAgent, session.ExpiresAt.UTC(),
	).Scan(&session.CreatedAt, &session.LastSeenAt)
	if err != nil {
		return translateError(err)
	}
	session.ID = id
	return nil
}

func (p *Postgres) GetAdminSessionByDigest(ctx context.Context, digest []byte) (*AdminSession, error) {
	return scanAdminSession(p.db.QueryRow(ctx, `SELECT `+adminSessionColumns+` FROM admin_sessions WHERE token_digest = $1`, digest))
}

func (p *Postgres) TouchAdminSession(ctx context.Context, id string, at time.Time) error {
	tag, err := p.db.Exec(ctx, `UPDATE admin_sessions SET last_seen_at = $2 WHERE id = $1`, id, ensureTime(at))
	if err != nil {
		return translateError(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (p *Postgres) DeleteAdminSessionByDigest(ctx context.Context, digest []byte) error {
	_, err := p.db.Exec(ctx, `DELETE FROM admin_sessions WHERE token_digest = $1`, digest)
	return translateError(err)
}

func (p *Postgres) DeleteAdminSessionsForAdmin(ctx context.Context, adminID string) error {
	_, err := p.db.Exec(ctx, `DELETE FROM admin_sessions WHERE admin_id = $1`, adminID)
	return translateError(err)
}

func (p *Postgres) DeleteExpiredAdminSessions(ctx context.Context, before time.Time) (int64, error) {
	tag, err := p.db.Exec(ctx, `DELETE FROM admin_sessions WHERE expires_at <= $1`, before.UTC())
	if err != nil {
		return 0, translateError(err)
	}
	return tag.RowsAffected(), nil
}

func scanAdmin(row rowScanner) (*AdminRecord, error) {
	var admin AdminRecord
	err := row.Scan(&admin.ID, &admin.Email, &admin.PasswordHash,
		&admin.TOTPSecretCipher, &admin.TOTPEnabled, &admin.PendingTOTPSecretCipher,
		&admin.PendingTOTPExpiresAt, &admin.Disabled, &admin.LastLoginAt,
		&admin.CreatedAt, &admin.UpdatedAt)
	if err != nil {
		return nil, translateError(err)
	}
	return &admin, nil
}

func scanAdminSession(row rowScanner) (*AdminSession, error) {
	var session AdminSession
	err := row.Scan(&session.ID, &session.AdminID, &session.TokenDigest,
		&session.CSRFDigest, &session.IPAddress, &session.UserAgent,
		&session.ExpiresAt, &session.LastSeenAt, &session.CreatedAt)
	if err != nil {
		return nil, translateError(err)
	}
	return &session, nil
}
