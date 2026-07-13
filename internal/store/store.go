package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/langrenjh-alt/GROK-GO/internal/security"
)

var (
	ErrNotFound = errors.New("record not found")
	ErrConflict = errors.New("record conflicts with existing data")
)

type dbtx interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type transaction interface {
	dbtx
	Commit(context.Context) error
	Rollback(context.Context) error
}

type Postgres struct {
	db      dbtx
	beginTx func(context.Context) (transaction, error)
}

func NewPostgres(pool *pgxpool.Pool) *Postgres {
	return &Postgres{
		db: pool,
		beginTx: func(ctx context.Context) (transaction, error) {
			return pool.Begin(ctx)
		},
	}
}

func (p *Postgres) Health(ctx context.Context) error {
	if p == nil || p.db == nil {
		return errors.New("PostgreSQL store is not configured")
	}
	var one int
	if err := p.db.QueryRow(ctx, `SELECT 1`).Scan(&one); err != nil {
		return fmt.Errorf("PostgreSQL health query: %w", err)
	}
	return nil
}

type Pagination struct {
	Limit  int
	Offset int
}

func (p Pagination) normalized() Pagination {
	if p.Limit <= 0 {
		p.Limit = 50
	}
	if p.Limit > 500 {
		p.Limit = 500
	}
	if p.Offset < 0 {
		p.Offset = 0
	}
	return p
}

type rowScanner interface {
	Scan(...any) error
}

func newID(existing string) (string, error) {
	if strings.TrimSpace(existing) != "" {
		return strings.TrimSpace(existing), nil
	}
	return security.GenerateID()
}

func marshalJSON(value any, fallback string) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("marshal JSON: %w", err)
	}
	if string(data) == "null" {
		return fallback, nil
	}
	return string(data), nil
}

func translateError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505", "23503", "23514":
			return fmt.Errorf("%w: %s", ErrConflict, pgErr.ConstraintName)
		}
	}
	return err
}

func nullableString(value string) any {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return value
}

func ensureTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Now().UTC()
	}
	return value.UTC()
}

func errorsNew(message string) error { return errors.New("store: " + message) }
