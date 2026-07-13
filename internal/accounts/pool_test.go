package accounts

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/langrenjh-alt/GROK-GO/internal/configevent"
	"github.com/langrenjh-alt/GROK-GO/internal/domain"
)

func TestPoolScoresLeasesAndKeepsAffinity(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	items := []domain.Account{
		{ID: "a", Kind: domain.CredentialCLIOAuth, Status: domain.AccountActive, Priority: 1, ConcurrencyLimit: 1, Quota: domain.QuotaSnapshot{RequestsLimit: 100, RequestsRemaining: 10}},
		{ID: "b", Kind: domain.CredentialCLIOAuth, Status: domain.AccountActive, Priority: 1, ConcurrencyLimit: 1, Quota: domain.QuotaSnapshot{RequestsLimit: 100, RequestsRemaining: 90}},
	}
	store := NewMemoryStore(items, map[string]domain.Credentials{"a": {AccessToken: "a"}, "b": {AccessToken: "b"}})
	pool := NewPool(store, DefaultPolicy())
	pool.now = func() time.Time { return now }
	model := domain.ModelSpec{ID: "grok", CredentialKinds: []domain.CredentialKind{domain.CredentialCLIOAuth}}

	first, err := pool.Acquire(context.Background(), Selection{Model: model, AffinityKey: "thread-1"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Account.ID != "b" {
		t.Fatalf("expected high-quota account b, got %s", first.Account.ID)
	}
	if err := first.Release(context.Background(), Feedback{StatusCode: 200}); err != nil {
		t.Fatal(err)
	}

	second, err := pool.Acquire(context.Background(), Selection{Model: model, AffinityKey: "thread-1"})
	if err != nil {
		t.Fatal(err)
	}
	if second.Account.ID != "b" {
		t.Fatalf("expected affinity to account b, got %s", second.Account.ID)
	}
	_ = second.Release(context.Background(), Feedback{StatusCode: 200})
}

func TestPoolEnforcesConcurrencyAndCooldown(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	account := domain.Account{ID: "a", Kind: domain.CredentialConsoleSSO, Status: domain.AccountActive, ConcurrencyLimit: 1}
	store := NewMemoryStore([]domain.Account{account}, map[string]domain.Credentials{"a": {SSO: "token"}})
	pool := NewPool(store, DefaultPolicy())
	pool.now = func() time.Time { return now }
	model := domain.ModelSpec{ID: "grok", CredentialKinds: []domain.CredentialKind{domain.CredentialConsoleSSO}}
	lease, err := pool.Acquire(context.Background(), Selection{Model: model})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Acquire(context.Background(), Selection{Model: model}); !errorsIs(err, ErrNoAccount) {
		t.Fatalf("expected concurrency exhaustion, got %v", err)
	}
	if err := lease.Release(context.Background(), Feedback{StatusCode: 429, RetryAfter: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Acquire(context.Background(), Selection{Model: model}); !errorsIs(err, ErrNoAccount) {
		t.Fatalf("expected cooldown, got %v", err)
	}
	now = now.Add(2 * time.Minute)
	if err := pool.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := pool.Acquire(context.Background(), Selection{Model: model})
	if err != nil {
		t.Fatal(err)
	}
	_ = got.Release(context.Background(), Feedback{StatusCode: 200})
}

func TestPoolSupportsPriorityAndRoundRobinStrategies(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	items := []domain.Account{
		{ID: "a", Kind: domain.CredentialCLIOAuth, Status: domain.AccountActive, Priority: 1, ConcurrencyLimit: 1, HealthScore: 1},
		{ID: "b", Kind: domain.CredentialCLIOAuth, Status: domain.AccountActive, Priority: 10, ConcurrencyLimit: 1, HealthScore: 1},
	}
	store := NewMemoryStore(items, map[string]domain.Credentials{"a": {AccessToken: "a"}, "b": {AccessToken: "b"}})
	policy := DefaultPolicy()
	policy.Strategy = StrategyPriority
	pool := NewPool(store, policy)
	pool.now = func() time.Time { return now }
	model := domain.ModelSpec{ID: "grok", CredentialKinds: []domain.CredentialKind{domain.CredentialCLIOAuth}}
	lease, err := pool.Acquire(context.Background(), Selection{Model: model, AffinityKey: "sticky"})
	if err != nil || lease.Account.ID != "b" {
		t.Fatalf("priority selection = %+v, %v", lease, err)
	}
	_ = lease.Release(context.Background(), Feedback{StatusCode: 200})

	a := store.Accounts["a"]
	a.Priority = 20
	store.Accounts["a"] = a
	b := store.Accounts["b"]
	b.Priority = 0
	store.Accounts["b"] = b
	if err := pool.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	lease, err = pool.Acquire(context.Background(), Selection{Model: model, AffinityKey: "sticky"})
	if err != nil || lease.Account.ID != "a" {
		t.Fatalf("priority strategy retained affinity: %+v, %v", lease, err)
	}
	_ = lease.Release(context.Background(), Feedback{StatusCode: 200})

	if err := pool.SetStrategy(StrategyRoundRobin); err != nil {
		t.Fatal(err)
	}
	var selected []string
	for range 4 {
		lease, err = pool.Acquire(context.Background(), Selection{Model: model})
		if err != nil {
			t.Fatal(err)
		}
		selected = append(selected, lease.Account.ID)
		_ = lease.Release(context.Background(), Feedback{StatusCode: 200})
	}
	if got := stringsJoin(selected); got != "a,b,a,b" {
		t.Fatalf("round-robin order = %s", got)
	}
}

func TestPoolPreferBestUsesHighestAvailableTier(t *testing.T) {
	items := []domain.Account{
		{ID: "basic", Kind: domain.CredentialGrokSSO, Tier: "basic", Status: domain.AccountActive, Priority: 100, ConcurrencyLimit: 1, HealthScore: 1},
		{ID: "super", Kind: domain.CredentialGrokSSO, Tier: "super", Status: domain.AccountActive, Priority: 10, ConcurrencyLimit: 1, HealthScore: 1},
		{ID: "heavy", Kind: domain.CredentialGrokSSO, Tier: "heavy", Status: domain.AccountActive, Priority: 1, ConcurrencyLimit: 1, HealthScore: 1},
	}
	credentials := map[string]domain.Credentials{
		"basic": {SSO: "basic"}, "super": {SSO: "super"}, "heavy": {SSO: "heavy"},
	}
	pool := NewPool(NewMemoryStore(items, credentials), DefaultPolicy())
	model := domain.ModelSpec{ID: "grok-4.20-fast", CredentialKinds: []domain.CredentialKind{domain.CredentialGrokSSO}, PreferBest: true}

	var leases []*Lease
	for _, want := range []string{"heavy", "super", "basic"} {
		lease, err := pool.Acquire(context.Background(), Selection{Model: model})
		if err != nil {
			t.Fatal(err)
		}
		leases = append(leases, lease)
		if lease.Account.ID != want {
			t.Fatalf("preferred account = %s, want %s", lease.Account.ID, want)
		}
	}
	for _, lease := range leases {
		_ = lease.Release(context.Background(), Feedback{StatusCode: 200})
	}
}

func TestPoolPersistsFailureStateBackoffAndRecovery(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	account := domain.Account{ID: "a", Kind: domain.CredentialGrokSSO, Status: domain.AccountActive, ConcurrencyLimit: 1, HealthScore: 1}
	store := NewMemoryStore([]domain.Account{account}, map[string]domain.Credentials{"a": {SSO: "token"}})
	pool := NewPool(store, DefaultPolicy())
	pool.now = func() time.Time { return now }
	model := domain.ModelSpec{ID: "grok", CredentialKinds: []domain.CredentialKind{domain.CredentialGrokSSO}}

	lease, err := pool.Acquire(context.Background(), Selection{Model: model})
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(context.Background(), Feedback{StatusCode: 503}); err != nil {
		t.Fatal(err)
	}
	failed := store.Accounts["a"]
	if failed.Status != domain.AccountCooldown || failed.FailureCount != 1 || failed.HealthScore >= 1 || failed.CooldownUntil == nil || !failed.CooldownUntil.Equal(now.Add(5*time.Second)) {
		t.Fatalf("transient failure state = %+v", failed)
	}
	now = now.Add(6 * time.Second)
	lease, err = pool.Acquire(context.Background(), Selection{Model: model})
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(context.Background(), Feedback{StatusCode: 200}); err != nil {
		t.Fatal(err)
	}
	recovered := store.Accounts["a"]
	if recovered.Status != domain.AccountActive || recovered.FailureCount != 0 || recovered.HealthScore <= failed.HealthScore || recovered.CooldownUntil != nil || recovered.LastError != "" {
		t.Fatalf("recovered state = %+v", recovered)
	}
}

func TestPoolDisablesPermanentCredentialFailuresAndFreezesExhaustedQuota(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	model := domain.ModelSpec{ID: "grok", CredentialKinds: []domain.CredentialKind{domain.CredentialCLIOAuth}}
	for _, status := range []int{401, 403, 423} {
		id := itoa(status)
		store := NewMemoryStore([]domain.Account{{ID: id, Kind: domain.CredentialCLIOAuth, Status: domain.AccountActive, ConcurrencyLimit: 1, HealthScore: 1}}, map[string]domain.Credentials{id: {AccessToken: "token"}})
		pool := NewPool(store, DefaultPolicy())
		pool.now = func() time.Time { return now }
		lease, err := pool.Acquire(context.Background(), Selection{Model: model})
		if err != nil {
			t.Fatal(err)
		}
		_ = lease.Release(context.Background(), Feedback{StatusCode: status})
		if got := store.Accounts[id]; got.Status != domain.AccountDisabled || got.FailureCount != 1 {
			t.Fatalf("status %d state = %+v", status, got)
		}
	}

	reset := now.Add(time.Hour)
	store := NewMemoryStore([]domain.Account{{ID: "quota", Kind: domain.CredentialCLIOAuth, Status: domain.AccountActive, ConcurrencyLimit: 1, HealthScore: 1}}, map[string]domain.Credentials{"quota": {AccessToken: "token"}})
	pool := NewPool(store, DefaultPolicy())
	pool.now = func() time.Time { return now }
	lease, err := pool.Acquire(context.Background(), Selection{Model: model})
	if err != nil {
		t.Fatal(err)
	}
	quota := domain.QuotaSnapshot{RequestsLimit: 10, RequestsRemaining: 0, ResetAt: &reset, ObservedAt: now}
	_ = lease.Release(context.Background(), Feedback{StatusCode: 200, Quota: &quota})
	got := store.Accounts["quota"]
	if got.Status != domain.AccountCooldown || got.CooldownUntil == nil || !got.CooldownUntil.Equal(reset) {
		t.Fatalf("quota exhaustion state = %+v", got)
	}
}

func TestPoolReloadAppliesManualDisableAndConcurrencyImmediately(t *testing.T) {
	account := domain.Account{ID: "a", Kind: domain.CredentialCLIOAuth, Status: domain.AccountActive, ConcurrencyLimit: 2, HealthScore: 1}
	store := NewMemoryStore([]domain.Account{account}, map[string]domain.Credentials{"a": {AccessToken: "token"}})
	pool := NewPool(store, DefaultPolicy())
	model := domain.ModelSpec{ID: "grok", CredentialKinds: []domain.CredentialKind{domain.CredentialCLIOAuth}}
	if err := pool.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	account.Status = domain.AccountDisabled
	account.ConcurrencyLimit = 1
	if err := store.UpdateAccount(context.Background(), account); err != nil {
		t.Fatal(err)
	}
	if err := pool.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Acquire(context.Background(), Selection{Model: model}); !errorsIs(err, ErrNoAccount) {
		t.Fatalf("disabled account remained schedulable: %v", err)
	}
	account.Status = domain.AccountActive
	if err := store.UpdateAccount(context.Background(), account); err != nil {
		t.Fatal(err)
	}
	if err := pool.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	first, err := pool.Acquire(context.Background(), Selection{Model: model})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Acquire(context.Background(), Selection{Model: model}); !errorsIs(err, ErrNoAccount) {
		t.Fatalf("updated concurrency limit was not applied: %v", err)
	}
	_ = first.Release(context.Background(), Feedback{StatusCode: 200})
}

func TestPoolSteadySuccessAvoidsPersistenceWrites(t *testing.T) {
	account := domain.Account{ID: "steady", Kind: domain.CredentialCLIOAuth, Status: domain.AccountActive, ConcurrencyLimit: 8, HealthScore: 1}
	store := &countingAccountStore{MemoryStore: NewMemoryStore([]domain.Account{account}, map[string]domain.Credentials{"steady": {AccessToken: "token"}})}
	pool := NewPool(store, DefaultPolicy())
	notifier := &recordingAccountNotifier{}
	pool.SetChangeNotifier(notifier)
	model := domain.ModelSpec{ID: "grok", CredentialKinds: []domain.CredentialKind{domain.CredentialCLIOAuth}}

	for range 10 {
		lease, err := pool.Acquire(context.Background(), Selection{Model: model})
		if err != nil {
			t.Fatal(err)
		}
		if err := lease.Release(context.Background(), Feedback{StatusCode: 200}); err != nil {
			t.Fatal(err)
		}
	}
	if store.updates != 0 {
		t.Fatalf("steady success persisted %d redundant account updates", store.updates)
	}
	if notifier.Count(configevent.ScopeAccounts) != 0 {
		t.Fatalf("steady success published %d redundant account notifications", notifier.Count(configevent.ScopeAccounts))
	}
}

func TestPoolPublishesPersistentFailureAndRecoveryChanges(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	account := domain.Account{ID: "notified", Kind: domain.CredentialCLIOAuth, Status: domain.AccountActive, ConcurrencyLimit: 1, HealthScore: 1}
	store := &countingAccountStore{MemoryStore: NewMemoryStore([]domain.Account{account}, map[string]domain.Credentials{"notified": {AccessToken: "token"}})}
	pool := NewPool(store, DefaultPolicy())
	pool.now = func() time.Time { return now }
	notifier := &recordingAccountNotifier{}
	pool.SetChangeNotifier(notifier)
	model := domain.ModelSpec{ID: "grok", CredentialKinds: []domain.CredentialKind{domain.CredentialCLIOAuth}}

	lease, err := pool.Acquire(context.Background(), Selection{Model: model})
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(context.Background(), Feedback{StatusCode: 429, RetryAfter: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if store.updates != 1 || notifier.Count(configevent.ScopeAccounts) != 1 {
		t.Fatalf("failure updates=%d account notifications=%d", store.updates, notifier.Count(configevent.ScopeAccounts))
	}

	now = now.Add(2 * time.Minute)
	lease, err = pool.Acquire(context.Background(), Selection{Model: model})
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(context.Background(), Feedback{StatusCode: 200}); err != nil {
		t.Fatal(err)
	}
	if store.updates != 2 || notifier.Count(configevent.ScopeAccounts) != 2 {
		t.Fatalf("recovery updates=%d account notifications=%d", store.updates, notifier.Count(configevent.ScopeAccounts))
	}
}

type countingAccountStore struct {
	*MemoryStore
	updates int
}

type recordingAccountNotifier struct {
	mu     sync.Mutex
	scopes []configevent.Scope
}

func (n *recordingAccountNotifier) Notify(_ context.Context, scope configevent.Scope) error {
	n.mu.Lock()
	n.scopes = append(n.scopes, scope)
	n.mu.Unlock()
	return nil
}

func (n *recordingAccountNotifier) Count(scope configevent.Scope) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	count := 0
	for _, item := range n.scopes {
		if item == scope {
			count++
		}
	}
	return count
}

func (s *countingAccountStore) UpdateAccount(ctx context.Context, account domain.Account) error {
	s.updates++
	return s.MemoryStore.UpdateAccount(ctx, account)
}

func stringsJoin(values []string) string {
	result := ""
	for index, value := range values {
		if index > 0 {
			result += ","
		}
		result += value
	}
	return result
}

func errorsIs(err, target error) bool { return err == target }
