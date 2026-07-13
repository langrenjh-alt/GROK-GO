package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/langrenjh-alt/GROK-GO/internal/domain"
)

const proxyColumns = `
	id, name, url_cipher, enabled, healthy, last_checked_at,
	last_error, created_at, updated_at`

func (p *Postgres) CreateProxy(ctx context.Context, proxy *domain.Proxy) error {
	if proxy == nil {
		return errorsNew("proxy is required")
	}
	id, err := newID(proxy.ID)
	if err != nil {
		return err
	}
	err = p.db.QueryRow(ctx, `
		INSERT INTO proxies (id, name, url_cipher, enabled, healthy, last_checked_at, last_error)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING created_at, updated_at`,
		id, strings.TrimSpace(proxy.Name), proxy.URLCipher, proxy.Enabled,
		proxy.Healthy, proxy.LastCheckedAt, proxy.LastError,
	).Scan(&proxy.CreatedAt, &proxy.UpdatedAt)
	if err != nil {
		return translateError(err)
	}
	proxy.ID = id
	return nil
}

func (p *Postgres) GetProxy(ctx context.Context, id string) (*domain.Proxy, error) {
	return scanProxy(p.db.QueryRow(ctx, `SELECT `+proxyColumns+` FROM proxies WHERE id = $1`, id))
}

func (p *Postgres) ListProxies(ctx context.Context, pagination Pagination) ([]domain.Proxy, error) {
	pagination = pagination.normalized()
	rows, err := p.db.Query(ctx, `SELECT `+proxyColumns+` FROM proxies ORDER BY name, created_at LIMIT $1 OFFSET $2`, pagination.Limit, pagination.Offset)
	if err != nil {
		return nil, fmt.Errorf("list proxies: %w", err)
	}
	defer rows.Close()
	proxies := make([]domain.Proxy, 0)
	for rows.Next() {
		proxy, err := scanProxy(rows)
		if err != nil {
			return nil, err
		}
		proxies = append(proxies, *proxy)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate proxies: %w", err)
	}
	return proxies, nil
}

func (p *Postgres) CountProxies(ctx context.Context) (int64, error) {
	var total int64
	if err := p.db.QueryRow(ctx, `SELECT COUNT(*) FROM proxies`).Scan(&total); err != nil {
		return 0, fmt.Errorf("count proxies: %w", err)
	}
	return total, nil
}

func (p *Postgres) UpdateProxy(ctx context.Context, proxy *domain.Proxy) error {
	if proxy == nil || strings.TrimSpace(proxy.ID) == "" {
		return errorsNew("proxy ID is required")
	}
	err := p.db.QueryRow(ctx, `
		UPDATE proxies SET name = $2, url_cipher = $3, enabled = $4,
			healthy = $5, last_checked_at = $6, last_error = $7, updated_at = now()
		WHERE id = $1 RETURNING updated_at`,
		proxy.ID, strings.TrimSpace(proxy.Name), proxy.URLCipher, proxy.Enabled,
		proxy.Healthy, proxy.LastCheckedAt, proxy.LastError,
	).Scan(&proxy.UpdatedAt)
	return translateError(err)
}

func (p *Postgres) DeleteProxy(ctx context.Context, id string) error {
	tag, err := p.db.Exec(ctx, `DELETE FROM proxies WHERE id = $1`, id)
	if err != nil {
		return translateError(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func scanProxy(row rowScanner) (*domain.Proxy, error) {
	var proxy domain.Proxy
	err := row.Scan(&proxy.ID, &proxy.Name, &proxy.URLCipher, &proxy.Enabled,
		&proxy.Healthy, &proxy.LastCheckedAt, &proxy.LastError, &proxy.CreatedAt, &proxy.UpdatedAt)
	if err != nil {
		return nil, translateError(err)
	}
	return &proxy, nil
}
