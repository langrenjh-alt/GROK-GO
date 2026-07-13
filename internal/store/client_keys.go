package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/langrenjh-alt/GROK-GO/internal/domain"
)

const clientKeyColumns = `
	id, name, prefix, digest, secret_cipher, enabled, rpm, concurrency_limit,
	daily_request_limit, monthly_token_limit, last_used_at, expires_at,
	created_at, updated_at`

func (p *Postgres) CreateClientKey(ctx context.Context, key *domain.ClientKey) error {
	if key == nil || len(key.Digest) == 0 {
		return errorsNew("client key and digest are required")
	}
	id, err := newID(key.ID)
	if err != nil {
		return err
	}
	err = p.db.QueryRow(ctx, `
		INSERT INTO client_keys (
			id, name, prefix, digest, secret_cipher, enabled, rpm, concurrency_limit,
			daily_request_limit, monthly_token_limit, last_used_at, expires_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		RETURNING created_at, updated_at`,
		id, strings.TrimSpace(key.Name), key.Prefix, key.Digest, key.SecretCipher, key.Enabled,
		key.RPM, key.ConcurrencyLimit, key.DailyRequestLimit, key.MonthlyTokenLimit,
		key.LastUsedAt, key.ExpiresAt,
	).Scan(&key.CreatedAt, &key.UpdatedAt)
	if err != nil {
		return translateError(err)
	}
	key.ID = id
	return nil
}

func (p *Postgres) GetClientKey(ctx context.Context, id string) (*domain.ClientKey, error) {
	return scanClientKey(p.db.QueryRow(ctx, `SELECT `+clientKeyColumns+` FROM client_keys WHERE id = $1`, id))
}

func (p *Postgres) GetClientKeyByDigest(ctx context.Context, digest []byte) (*domain.ClientKey, error) {
	return scanClientKey(p.db.QueryRow(ctx, `SELECT `+clientKeyColumns+` FROM client_keys WHERE digest = $1`, digest))
}

func (p *Postgres) ListClientKeys(ctx context.Context, pagination Pagination) ([]domain.ClientKey, error) {
	pagination = pagination.normalized()
	rows, err := p.db.Query(ctx, `SELECT `+clientKeyColumns+` FROM client_keys ORDER BY created_at DESC LIMIT $1 OFFSET $2`, pagination.Limit, pagination.Offset)
	if err != nil {
		return nil, fmt.Errorf("list client keys: %w", err)
	}
	defer rows.Close()
	keys := make([]domain.ClientKey, 0)
	for rows.Next() {
		key, err := scanClientKey(rows)
		if err != nil {
			return nil, err
		}
		keys = append(keys, *key)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate client keys: %w", err)
	}
	return keys, nil
}

func (p *Postgres) CountClientKeys(ctx context.Context) (int64, error) {
	var total int64
	if err := p.db.QueryRow(ctx, `SELECT COUNT(*) FROM client_keys`).Scan(&total); err != nil {
		return 0, fmt.Errorf("count client keys: %w", err)
	}
	return total, nil
}

func (p *Postgres) CountActiveClientKeys(ctx context.Context, now time.Time) (int64, error) {
	var total int64
	if err := p.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM client_keys
		WHERE enabled AND (expires_at IS NULL OR expires_at > $1)`, ensureTime(now)).Scan(&total); err != nil {
		return 0, fmt.Errorf("count active client keys: %w", err)
	}
	return total, nil
}

func (p *Postgres) UpdateClientKey(ctx context.Context, key *domain.ClientKey) error {
	if key == nil || strings.TrimSpace(key.ID) == "" {
		return errorsNew("client key ID is required")
	}
	err := p.db.QueryRow(ctx, `
		UPDATE client_keys SET name = $2, enabled = $3, rpm = $4,
			concurrency_limit = $5, daily_request_limit = $6,
			monthly_token_limit = $7, expires_at = $8, updated_at = now()
		WHERE id = $1 RETURNING updated_at`,
		key.ID, strings.TrimSpace(key.Name), key.Enabled, key.RPM,
		key.ConcurrencyLimit, key.DailyRequestLimit, key.MonthlyTokenLimit, key.ExpiresAt,
	).Scan(&key.UpdatedAt)
	return translateError(err)
}

func (p *Postgres) TouchClientKey(ctx context.Context, id string, usedAt time.Time) error {
	tag, err := p.db.Exec(ctx, `UPDATE client_keys SET last_used_at = $2 WHERE id = $1`, id, ensureTime(usedAt))
	if err != nil {
		return translateError(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (p *Postgres) DeleteClientKey(ctx context.Context, id string) error {
	tag, err := p.db.Exec(ctx, `DELETE FROM client_keys WHERE id = $1`, id)
	if err != nil {
		return translateError(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func scanClientKey(row rowScanner) (*domain.ClientKey, error) {
	var key domain.ClientKey
	err := row.Scan(&key.ID, &key.Name, &key.Prefix, &key.Digest, &key.SecretCipher, &key.Enabled,
		&key.RPM, &key.ConcurrencyLimit, &key.DailyRequestLimit, &key.MonthlyTokenLimit,
		&key.LastUsedAt, &key.ExpiresAt, &key.CreatedAt, &key.UpdatedAt)
	if err != nil {
		return nil, translateError(err)
	}
	return &key, nil
}
