package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/langrenjh-alt/GROK-GO/internal/domain"
)

func TestUpdateAccountsCommitsAllUpdatesInOneTransaction(t *testing.T) {
	tx := &fakeAccountTransaction{}
	repository := &Postgres{beginTx: func(context.Context) (transaction, error) { return tx, nil }}
	accounts := []*domain.Account{batchAccount("first"), batchAccount("second")}

	if err := repository.UpdateAccounts(context.Background(), accounts); err != nil {
		t.Fatal(err)
	}
	if !tx.committed || len(tx.persisted) != 2 || tx.persisted[0] != "first" || tx.persisted[1] != "second" {
		t.Fatalf("transaction = %+v", tx)
	}
	for _, account := range accounts {
		if account.UpdatedAt.IsZero() {
			t.Fatalf("account %s did not receive updated_at", account.ID)
		}
	}
}

func TestUpdateAccountsRollsBackEveryUpdateWhenOneWriteFails(t *testing.T) {
	tx := &fakeAccountTransaction{failAt: 2}
	repository := &Postgres{beginTx: func(context.Context) (transaction, error) { return tx, nil }}
	err := repository.UpdateAccounts(context.Background(), []*domain.Account{batchAccount("first"), batchAccount("second")})
	if err == nil {
		t.Fatal("batch update succeeded after a forced second-write failure")
	}
	if tx.committed || !tx.rolledBack || len(tx.persisted) != 0 || len(tx.staged) != 0 {
		t.Fatalf("failed transaction was not rolled back: %+v", tx)
	}
}

func TestUpdateAccountsRollsBackWhenCommitFails(t *testing.T) {
	tx := &fakeAccountTransaction{commitErr: errors.New("commit failed")}
	repository := &Postgres{beginTx: func(context.Context) (transaction, error) { return tx, nil }}
	err := repository.UpdateAccounts(context.Background(), []*domain.Account{batchAccount("first"), batchAccount("second")})
	if err == nil || !tx.rolledBack || len(tx.persisted) != 0 {
		t.Fatalf("commit failure did not roll back: err=%v tx=%+v", err, tx)
	}
}

func batchAccount(id string) *domain.Account {
	return &domain.Account{
		ID: id, Name: id, Kind: domain.CredentialGrokSSO, Tier: "basic",
		Status: domain.AccountActive, CredentialCipher: []byte("cipher"),
		ConcurrencyLimit: 1, HealthScore: 1,
	}
}

type fakeAccountTransaction struct {
	queryCalls int
	failAt     int
	commitErr  error
	staged     []string
	persisted  []string
	committed  bool
	rolledBack bool
}

func (t *fakeAccountTransaction) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errors.New("unexpected Exec")
}

func (t *fakeAccountTransaction) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("unexpected Query")
}

func (t *fakeAccountTransaction) QueryRow(_ context.Context, _ string, args ...any) pgx.Row {
	t.queryCalls++
	if t.failAt > 0 && t.queryCalls == t.failAt {
		return fakeAccountUpdateRow{err: errors.New("forced update failure")}
	}
	id, _ := args[0].(string)
	t.staged = append(t.staged, id)
	return fakeAccountUpdateRow{updatedAt: time.Date(2026, 7, 14, 8, 0, t.queryCalls, 0, time.UTC)}
}

func (t *fakeAccountTransaction) Commit(context.Context) error {
	if t.commitErr != nil {
		return t.commitErr
	}
	t.persisted = append(t.persisted, t.staged...)
	t.staged = nil
	t.committed = true
	return nil
}

func (t *fakeAccountTransaction) Rollback(context.Context) error {
	if t.committed {
		return pgx.ErrTxClosed
	}
	t.staged = nil
	t.rolledBack = true
	return nil
}

type fakeAccountUpdateRow struct {
	updatedAt time.Time
	err       error
}

func (r fakeAccountUpdateRow) Scan(destinations ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(destinations) != 1 {
		return errors.New("unexpected scan destination count")
	}
	updatedAt, ok := destinations[0].(*time.Time)
	if !ok {
		return errors.New("unexpected scan destination")
	}
	*updatedAt = r.updatedAt
	return nil
}
