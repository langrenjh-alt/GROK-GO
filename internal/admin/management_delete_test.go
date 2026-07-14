package admin

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/langrenjh-alt/GROK-GO/internal/domain"
	"github.com/langrenjh-alt/GROK-GO/internal/store"
)

func TestManagementDeleteAccountsValidatesDeduplicatesAndDeletesAtomically(t *testing.T) {
	t.Run("empty selection", func(t *testing.T) {
		repository, service := newBatchDeleteManagementService("first")
		if _, err := service.DeleteAccounts(context.Background(), nil); err == nil {
			t.Fatal("DeleteAccounts accepted an empty selection")
		}
		if repository.deleteBatchCalls != 0 || len(repository.values) != 1 {
			t.Fatalf("empty selection changed repository: calls=%d values=%#v", repository.deleteBatchCalls, repository.values)
		}
	})

	t.Run("duplicate IDs", func(t *testing.T) {
		repository, service := newBatchDeleteManagementService("first", "second")
		deleted, err := service.DeleteAccounts(context.Background(), []string{" first ", "first", "second", " "})
		if err != nil {
			t.Fatal(err)
		}
		if deleted != 2 || repository.deleteBatchCalls != 1 || len(repository.values) != 0 {
			t.Fatalf("batch result = deleted:%d calls:%d values:%#v", deleted, repository.deleteBatchCalls, repository.values)
		}
		if !slices.Equal(repository.deleteBatchIDs, []string{"first", "second"}) {
			t.Fatalf("repository IDs = %v", repository.deleteBatchIDs)
		}
	})

	t.Run("missing ID is atomic", func(t *testing.T) {
		repository, service := newBatchDeleteManagementService("first", "second")
		deleted, err := service.DeleteAccounts(context.Background(), []string{"first", "missing"})
		if !errors.Is(err, store.ErrNotFound) || deleted != 0 {
			t.Fatalf("DeleteAccounts = %d, %v", deleted, err)
		}
		if repository.deleteBatchCalls != 1 || len(repository.values) != 2 {
			t.Fatalf("failed batch changed repository: calls=%d values=%#v", repository.deleteBatchCalls, repository.values)
		}
		for _, id := range []string{"first", "second"} {
			if _, ok := repository.values[id]; !ok {
				t.Fatalf("account %q was partially deleted", id)
			}
		}
	})
}

func newBatchDeleteManagementService(ids ...string) (*fakeAccountRepo, *ManagementService) {
	repository := &fakeAccountRepo{values: make(map[string]*domain.Account, len(ids))}
	for _, id := range ids {
		repository.values[id] = &domain.Account{ID: id, Name: id}
	}
	return repository, NewManagementService(repository, nil, nil, nil, nil, nil, nil)
}

func (r *fakeAccountRepo) DeleteAccounts(_ context.Context, ids []string) error {
	r.deleteBatchCalls++
	r.deleteBatchIDs = append([]string(nil), ids...)
	for _, id := range ids {
		if _, ok := r.values[id]; !ok {
			return store.ErrNotFound
		}
	}
	for _, id := range ids {
		delete(r.values, id)
	}
	return nil
}
