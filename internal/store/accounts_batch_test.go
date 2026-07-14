package store

import (
	"context"
	"errors"
	"strconv"
	"strings"
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

func TestDeleteAccountsCommitsMatchingRowsInOneTransaction(t *testing.T) {
	tx := &fakeAccountDeleteTransaction{rowsAffected: 2}
	repository := &Postgres{beginTx: func(context.Context) (transaction, error) { return tx, nil }}
	ids := []string{"first", "second"}

	if err := repository.DeleteAccounts(context.Background(), ids); err != nil {
		t.Fatal(err)
	}
	if !tx.committed || tx.rolledBack {
		t.Fatalf("delete transaction did not commit cleanly: %+v", tx)
	}
	if strings.Join(tx.persisted, ",") != "first,second" {
		t.Fatalf("persisted deletions = %v, want %v", tx.persisted, ids)
	}
}

func TestDeleteAccountsRollsBackWhenAnyIDIsMissing(t *testing.T) {
	tx := &fakeAccountDeleteTransaction{rowsAffected: 1}
	repository := &Postgres{beginTx: func(context.Context) (transaction, error) { return tx, nil }}

	err := repository.DeleteAccounts(context.Background(), []string{"present", "missing"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteAccounts() error = %v, want ErrNotFound", err)
	}
	if tx.committed || !tx.rolledBack || len(tx.staged) != 0 || len(tx.persisted) != 0 {
		t.Fatalf("partial delete was not rolled back: %+v", tx)
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

type fakeAccountDeleteTransaction struct {
	rowsAffected int64
	staged       []string
	persisted    []string
	committed    bool
	rolledBack   bool
}

func (t *fakeAccountDeleteTransaction) Exec(_ context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	if !strings.Contains(query, "DELETE FROM accounts WHERE id = ANY($1::text[])") {
		return pgconn.CommandTag{}, errors.New("unexpected delete query")
	}
	if len(args) != 1 {
		return pgconn.CommandTag{}, errors.New("unexpected delete argument count")
	}
	ids, ok := args[0].([]string)
	if !ok {
		return pgconn.CommandTag{}, errors.New("unexpected delete argument")
	}
	t.staged = append(t.staged, ids...)
	return pgconn.NewCommandTag("DELETE " + strconv.FormatInt(t.rowsAffected, 10)), nil
}

func (t *fakeAccountDeleteTransaction) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("unexpected Query")
}

func (t *fakeAccountDeleteTransaction) QueryRow(context.Context, string, ...any) pgx.Row {
	return fakeAccountUpdateRow{err: errors.New("unexpected QueryRow")}
}

func (t *fakeAccountDeleteTransaction) Commit(context.Context) error {
	t.persisted = append(t.persisted, t.staged...)
	t.staged = nil
	t.committed = true
	return nil
}

func (t *fakeAccountDeleteTransaction) Rollback(context.Context) error {
	if t.committed {
		return pgx.ErrTxClosed
	}
	t.staged = nil
	t.rolledBack = true
	return nil
}
