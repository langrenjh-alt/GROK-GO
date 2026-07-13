package accounts

import (
	"context"
	"errors"
	"sync"

	"github.com/langrenjh-alt/GROK-GO/internal/domain"
)

// MemoryStore is useful for embedding and tests; production stores can satisfy
// Store without exposing encryption details to the pool.
type MemoryStore struct {
	mu                   sync.RWMutex
	Accounts             map[string]domain.Account
	CredentialsByAccount map[string]domain.Credentials
}

func NewMemoryStore(items []domain.Account, credentials map[string]domain.Credentials) *MemoryStore {
	store := &MemoryStore{Accounts: make(map[string]domain.Account), CredentialsByAccount: make(map[string]domain.Credentials)}
	for _, item := range items {
		store.Accounts[item.ID] = item
	}
	for id, value := range credentials {
		store.CredentialsByAccount[id] = value
	}
	return store
}

func (s *MemoryStore) ListAccounts(context.Context) ([]domain.Account, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]domain.Account, 0, len(s.Accounts))
	for _, item := range s.Accounts {
		result = append(result, item)
	}
	return result, nil
}

func (s *MemoryStore) Credentials(_ context.Context, accountID string) (domain.Credentials, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.CredentialsByAccount[accountID]
	if !ok {
		return domain.Credentials{}, errors.New("credentials not found")
	}
	return value, nil
}

func (s *MemoryStore) GetAccount(_ context.Context, accountID string) (*domain.Account, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.Accounts[accountID]
	if !ok {
		return nil, errors.New("account not found")
	}
	return &value, nil
}

func (s *MemoryStore) UpdateAccount(_ context.Context, account domain.Account) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Accounts[account.ID] = account
	return nil
}
