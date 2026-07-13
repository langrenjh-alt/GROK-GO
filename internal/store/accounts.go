package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/langrenjh-alt/GROK-GO/internal/domain"
)

const accountColumns = `
	id, name, kind, tier, status, email, credential_cipher, proxy_id,
	models, tags, priority, concurrency_limit, health_score, failure_count, quota, cooldown_until,
	last_used_at, last_error, created_at, updated_at`

type AccountFilter struct {
	Pagination
	Kind   domain.CredentialKind
	Status domain.AccountStatus
	Model  string
	Query  string
}

func (p *Postgres) CreateAccount(ctx context.Context, account *domain.Account) error {
	if account == nil {
		return errorsNew("account is required")
	}
	if account.Status == "" {
		account.Status = domain.AccountActive
	}
	if account.ConcurrencyLimit <= 0 {
		account.ConcurrencyLimit = 1
	}
	if account.HealthScore <= 0 {
		account.HealthScore = 1
	}
	id, err := newID(account.ID)
	if err != nil {
		return err
	}
	models, err := marshalJSON(account.Models, "[]")
	if err != nil {
		return err
	}
	tags, err := marshalJSON(account.Tags, "[]")
	if err != nil {
		return err
	}
	quota, err := marshalJSON(account.Quota, "{}")
	if err != nil {
		return err
	}
	err = p.db.QueryRow(ctx, `
		INSERT INTO accounts (
			id, name, kind, tier, status, email, credential_cipher, proxy_id,
			models, tags, priority, concurrency_limit, health_score, failure_count, quota, cooldown_until,
			last_used_at, last_error
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8,
			$9::jsonb, $10::jsonb, $11, $12, $13, $14, $15::jsonb, $16,
			$17, $18
		)
		RETURNING created_at, updated_at`,
		id, strings.TrimSpace(account.Name), account.Kind, account.Tier, account.Status,
		account.Email, account.CredentialCipher, nullableString(account.ProxyID), models, tags,
		account.Priority, account.ConcurrencyLimit, account.HealthScore, account.FailureCount,
		quota, account.CooldownUntil, account.LastUsedAt, account.LastError,
	).Scan(&account.CreatedAt, &account.UpdatedAt)
	if err != nil {
		return translateError(err)
	}
	account.ID = id
	return nil
}

func (p *Postgres) GetAccount(ctx context.Context, id string) (*domain.Account, error) {
	return scanAccount(p.db.QueryRow(ctx, `SELECT `+accountColumns+` FROM accounts WHERE id = $1`, id))
}

func (p *Postgres) ListAccounts(ctx context.Context, filter AccountFilter) ([]domain.Account, error) {
	filter.Pagination = filter.Pagination.normalized()
	where, args := accountFilterSQL(filter)
	query := `SELECT ` + accountColumns + ` FROM accounts ` + where
	args = append(args, filter.Limit, filter.Offset)
	query += fmt.Sprintf(" ORDER BY priority DESC, created_at ASC LIMIT $%d OFFSET $%d", len(args)-1, len(args))
	rows, err := p.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list accounts: %w", err)
	}
	defer rows.Close()
	accounts := make([]domain.Account, 0)
	for rows.Next() {
		account, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		accounts = append(accounts, *account)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate accounts: %w", err)
	}
	return accounts, nil
}

func (p *Postgres) CountAccounts(ctx context.Context, filter AccountFilter) (int64, error) {
	where, args := accountFilterSQL(filter)
	var total int64
	if err := p.db.QueryRow(ctx, `SELECT COUNT(*) FROM accounts `+where, args...).Scan(&total); err != nil {
		return 0, fmt.Errorf("count accounts: %w", err)
	}
	return total, nil
}

func accountFilterSQL(filter AccountFilter) (string, []any) {
	query := "WHERE TRUE"
	args := make([]any, 0, 4)
	add := func(condition string, value any) {
		args = append(args, value)
		query += " AND " + fmt.Sprintf(condition, len(args))
	}
	if filter.Kind != "" {
		add("kind = $%d", filter.Kind)
	}
	if filter.Status != "" {
		add("status = $%d", filter.Status)
	}
	if model := strings.TrimSpace(filter.Model); model != "" {
		encoded, _ := json.Marshal([]string{model})
		add("models @> $%d::jsonb", string(encoded))
	}
	if search := strings.TrimSpace(filter.Query); search != "" {
		args = append(args, "%"+search+"%")
		index := len(args)
		query += fmt.Sprintf(" AND (name ILIKE $%d OR email ILIKE $%d OR tags::text ILIKE $%d)", index, index, index)
	}
	return query, args
}

func (p *Postgres) UpdateAccount(ctx context.Context, account *domain.Account) error {
	return updateAccount(ctx, p.db, account)
}

func (p *Postgres) UpdateAccounts(ctx context.Context, accounts []*domain.Account) error {
	if len(accounts) == 0 {
		return errorsNew("at least one account is required")
	}
	if p.beginTx == nil {
		return errorsNew("account transactions are not configured")
	}
	tx, err := p.beginTx(ctx)
	if err != nil {
		return fmt.Errorf("begin account update transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	for _, account := range accounts {
		if err := updateAccount(ctx, tx, account); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return translateError(err)
	}
	return nil
}

func updateAccount(ctx context.Context, database dbtx, account *domain.Account) error {
	if account == nil || strings.TrimSpace(account.ID) == "" {
		return errorsNew("account ID is required")
	}
	models, err := marshalJSON(account.Models, "[]")
	if err != nil {
		return err
	}
	tags, err := marshalJSON(account.Tags, "[]")
	if err != nil {
		return err
	}
	quota, err := marshalJSON(account.Quota, "{}")
	if err != nil {
		return err
	}
	err = database.QueryRow(ctx, `
		UPDATE accounts SET
			name = $2, kind = $3, tier = $4, status = $5, email = $6,
			credential_cipher = $7, proxy_id = $8, models = $9::jsonb,
			tags = $10::jsonb, priority = $11, concurrency_limit = $12,
			health_score = $13, failure_count = $14, quota = $15::jsonb,
			cooldown_until = $16, last_used_at = $17,
			last_error = $18, updated_at = now()
		WHERE id = $1 RETURNING updated_at`,
		account.ID, strings.TrimSpace(account.Name), account.Kind, account.Tier,
		account.Status, account.Email, account.CredentialCipher, nullableString(account.ProxyID),
		models, tags, account.Priority, account.ConcurrencyLimit, account.HealthScore,
		account.FailureCount, quota, account.CooldownUntil, account.LastUsedAt, account.LastError,
	).Scan(&account.UpdatedAt)
	return translateError(err)
}

func (p *Postgres) DeleteAccount(ctx context.Context, id string) error {
	tag, err := p.db.Exec(ctx, `DELETE FROM accounts WHERE id = $1`, id)
	if err != nil {
		return translateError(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func scanAccount(row rowScanner) (*domain.Account, error) {
	var account domain.Account
	var kind, status string
	var proxyID *string
	var models, tags, quota []byte
	err := row.Scan(
		&account.ID, &account.Name, &kind, &account.Tier, &status, &account.Email,
		&account.CredentialCipher, &proxyID, &models, &tags, &account.Priority,
		&account.ConcurrencyLimit, &account.HealthScore, &account.FailureCount, &quota,
		&account.CooldownUntil, &account.LastUsedAt,
		&account.LastError, &account.CreatedAt, &account.UpdatedAt,
	)
	if err != nil {
		return nil, translateError(err)
	}
	account.Kind = domain.CredentialKind(kind)
	account.Status = domain.AccountStatus(status)
	if proxyID != nil {
		account.ProxyID = *proxyID
	}
	if err := json.Unmarshal(models, &account.Models); err != nil {
		return nil, fmt.Errorf("decode account models: %w", err)
	}
	if err := json.Unmarshal(tags, &account.Tags); err != nil {
		return nil, fmt.Errorf("decode account tags: %w", err)
	}
	if err := json.Unmarshal(quota, &account.Quota); err != nil {
		return nil, fmt.Errorf("decode account quota: %w", err)
	}
	return &account, nil
}
