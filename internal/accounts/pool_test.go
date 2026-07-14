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
	if !first.CacheIdentityApplied || first.AffinityReused || !first.CacheAffinityEstablished {
		t.Fatalf("first affinity lease flags = applied:%v reused:%v established:%v", first.CacheIdentityApplied, first.AffinityReused, first.CacheAffinityEstablished)
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
	if !second.CacheIdentityApplied || !second.AffinityReused || second.CacheAffinityEstablished {
		t.Fatalf("reused affinity lease flags = applied:%v reused:%v established:%v", second.CacheIdentityApplied, second.AffinityReused, second.CacheAffinityEstablished)
	}
	_ = second.Release(context.Background(), Feedback{StatusCode: 200})
}

func TestPoolDoesNotReportAffinityForNonAffinityStrategies(t *testing.T) {
	for _, strategy := range []Strategy{StrategyPriority, StrategyRoundRobin} {
		t.Run(string(strategy), func(t *testing.T) {
			account := domain.Account{ID: "account", Kind: domain.CredentialCLIOAuth, Status: domain.AccountActive, ConcurrencyLimit: 1, HealthScore: 1}
			store := NewMemoryStore([]domain.Account{account}, map[string]domain.Credentials{account.ID: {AccessToken: "token"}})
			policy := DefaultPolicy()
			policy.Strategy = strategy
			pool := NewPool(store, policy)
			lease, err := pool.Acquire(context.Background(), Selection{
				Model:       domain.ModelSpec{ID: "grok", CredentialKinds: []domain.CredentialKind{domain.CredentialCLIOAuth}},
				AffinityKey: "cache-identity",
			})
			if err != nil {
				t.Fatal(err)
			}
			if lease.CacheIdentityApplied || lease.AffinityReused || lease.CacheAffinityEstablished {
				t.Fatalf("non-affinity strategy flags = applied:%v reused:%v established:%v", lease.CacheIdentityApplied, lease.AffinityReused, lease.CacheAffinityEstablished)
			}
			_ = lease.Release(context.Background(), Feedback{StatusCode: 200})
		})
	}
}

func TestPoolScoreUsesLowestBoundedRequestOrTokenQuota(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	items := []domain.Account{
		{ID: "token-low", Kind: domain.CredentialCLIOAuth, Status: domain.AccountActive, ConcurrencyLimit: 1, HealthScore: 1, Quota: domain.QuotaSnapshot{RequestsLimit: 100, RequestsRemaining: 90, TokensLimit: 1000, TokensRemaining: 10}},
		{ID: "balanced", Kind: domain.CredentialCLIOAuth, Status: domain.AccountActive, ConcurrencyLimit: 1, HealthScore: 1, Quota: domain.QuotaSnapshot{RequestsLimit: 100, RequestsRemaining: 60, TokensLimit: 1000, TokensRemaining: 600}},
	}
	store := NewMemoryStore(items, map[string]domain.Credentials{"token-low": {AccessToken: "a"}, "balanced": {AccessToken: "b"}})
	pool := NewPool(store, DefaultPolicy())
	pool.now = func() time.Time { return now }
	model := domain.ModelSpec{ID: "grok", CredentialKinds: []domain.CredentialKind{domain.CredentialCLIOAuth}}
	lease, err := pool.Acquire(context.Background(), Selection{Model: model})
	if err != nil {
		t.Fatal(err)
	}
	if lease.Account.ID != "balanced" {
		t.Fatalf("quota-aware selection chose %q, want balanced", lease.Account.ID)
	}
	_ = lease.Release(context.Background(), Feedback{StatusCode: 200})
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

func TestPoolConcurrentSuccessDoesNotEraseRateLimitCooldown(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	account := domain.Account{ID: "a", Kind: domain.CredentialCLIOAuth, Status: domain.AccountActive, ConcurrencyLimit: 2, HealthScore: 1}
	store := NewMemoryStore([]domain.Account{account}, map[string]domain.Credentials{"a": {AccessToken: "token"}})
	pool := NewPool(store, DefaultPolicy())
	pool.now = func() time.Time { return now }
	model := domain.ModelSpec{ID: "grok", CredentialKinds: []domain.CredentialKind{domain.CredentialCLIOAuth}}

	limited, err := pool.Acquire(context.Background(), Selection{Model: model})
	if err != nil {
		t.Fatal(err)
	}
	olderSuccess, err := pool.Acquire(context.Background(), Selection{Model: model})
	if err != nil {
		t.Fatal(err)
	}
	if err := limited.Release(context.Background(), Feedback{StatusCode: 429, RetryAfter: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if err := olderSuccess.Release(context.Background(), Feedback{StatusCode: 200}); err != nil {
		t.Fatal(err)
	}

	stored := store.Accounts["a"]
	if stored.Status != domain.AccountCooldown || stored.CooldownUntil == nil || !stored.CooldownUntil.Equal(now.Add(time.Minute)) || stored.FailureCount != 1 {
		t.Fatalf("older success erased concurrent cooldown: %+v", stored)
	}
	if _, err := pool.Acquire(context.Background(), Selection{Model: model}); !errorsIs(err, ErrNoAccount) {
		t.Fatalf("account became schedulable during preserved cooldown: %v", err)
	}
}

func TestPoolIgnoresFeedbackFromReplacedCredentials(t *testing.T) {
	account := domain.Account{
		ID: "a", Kind: domain.CredentialCLIOAuth, Status: domain.AccountActive,
		ConcurrencyLimit: 1, HealthScore: 1, CredentialFingerprint: []byte("old-fingerprint"),
	}
	store := NewMemoryStore([]domain.Account{account}, map[string]domain.Credentials{"a": {AccessToken: "old-token"}})
	pool := NewPool(store, DefaultPolicy())
	model := domain.ModelSpec{ID: "grok", CredentialKinds: []domain.CredentialKind{domain.CredentialCLIOAuth}}
	lease, err := pool.Acquire(context.Background(), Selection{Model: model})
	if err != nil {
		t.Fatal(err)
	}

	account.CredentialFingerprint = []byte("new-fingerprint")
	store.SetCredentials(account.ID, domain.Credentials{AccessToken: "new-token"})
	if err := store.UpdateAccount(context.Background(), account); err != nil {
		t.Fatal(err)
	}
	if err := pool.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(context.Background(), Feedback{StatusCode: 401}); err != nil {
		t.Fatal(err)
	}
	stored := store.Accounts[account.ID]
	if stored.Status != domain.AccountActive || stored.FailureCount != 0 || stored.LastError != "" {
		t.Fatalf("stale credential feedback changed refreshed account: %+v", stored)
	}
}

func TestPoolIgnoresFeedbackAfterAccessTokenOnlyRotation(t *testing.T) {
	account := domain.Account{
		ID: "a", Kind: domain.CredentialCLIOAuth, Status: domain.AccountActive,
		ConcurrencyLimit: 1, HealthScore: 1,
		CredentialFingerprint: []byte("stable-refresh-token-fingerprint"),
		CredentialCipher:      []byte("sealed-old-access-token"),
	}
	store := NewMemoryStore([]domain.Account{account}, map[string]domain.Credentials{"a": {AccessToken: "old-token", RefreshToken: "stable-refresh-token"}})
	pool := NewPool(store, DefaultPolicy())
	model := domain.ModelSpec{ID: "grok", CredentialKinds: []domain.CredentialKind{domain.CredentialCLIOAuth}}
	lease, err := pool.Acquire(context.Background(), Selection{Model: model})
	if err != nil {
		t.Fatal(err)
	}

	account.CredentialCipher = []byte("sealed-new-access-token")
	store.SetCredentials(account.ID, domain.Credentials{AccessToken: "new-token", RefreshToken: "stable-refresh-token"})
	if err := store.UpdateAccount(context.Background(), account); err != nil {
		t.Fatal(err)
	}
	if err := pool.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(context.Background(), Feedback{StatusCode: 401}); err != nil {
		t.Fatal(err)
	}
	stored := store.Accounts[account.ID]
	if stored.Status != domain.AccountActive || stored.FailureCount != 0 || stored.LastError != "" {
		t.Fatalf("old access-token feedback changed the refreshed account: %+v", stored)
	}
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
	if failed.Status != domain.AccountCooldown || failed.FailureCount != 1 || failed.HealthScore >= 1 || failed.CooldownUntil == nil || !failed.CooldownUntil.Equal(now.Add(5*time.Second)) || failed.LastError != "upstream status 503" {
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

	storeWithQuota := NewMemoryStore([]domain.Account{{ID: "credential-and-quota", Kind: domain.CredentialCLIOAuth, Status: domain.AccountActive, ConcurrencyLimit: 1, HealthScore: 1}}, map[string]domain.Credentials{"credential-and-quota": {AccessToken: "token"}})
	poolWithQuota := NewPool(storeWithQuota, DefaultPolicy())
	poolWithQuota.now = func() time.Time { return now }
	credentialLease, err := poolWithQuota.Acquire(context.Background(), Selection{Model: model})
	if err != nil {
		t.Fatal(err)
	}
	credentialReset := now.Add(time.Hour)
	credentialQuota := domain.QuotaSnapshot{RequestsLimit: 10, RequestsRemaining: 0, ResetAt: &credentialReset, ObservedAt: now}
	if err := credentialLease.Release(context.Background(), Feedback{StatusCode: 401, Quota: &credentialQuota}); err != nil {
		t.Fatal(err)
	}
	if got := storeWithQuota.Accounts["credential-and-quota"]; got.Status != domain.AccountDisabled || got.CooldownUntil != nil {
		t.Fatalf("permanent credential failure was hidden by exhausted quota: %+v", got)
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

func TestPoolBlocksTokenQuotaAndUsesFallbackWhenResetIsMissing(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	reset := now.Add(time.Hour)
	model := domain.ModelSpec{ID: "grok", CredentialKinds: []domain.CredentialKind{domain.CredentialCLIOAuth}}
	account := domain.Account{
		ID: "token-quota", Kind: domain.CredentialCLIOAuth, Status: domain.AccountActive,
		ConcurrencyLimit: 1, HealthScore: 1,
		Quota: domain.QuotaSnapshot{TokensLimit: 1000, TokensRemaining: 0, ResetAt: &reset, ObservedAt: now},
	}
	store := NewMemoryStore([]domain.Account{account}, map[string]domain.Credentials{"token-quota": {AccessToken: "token"}})
	pool := NewPool(store, DefaultPolicy())
	pool.now = func() time.Time { return now }
	if _, err := pool.Acquire(context.Background(), Selection{Model: model}); !errorsIs(err, ErrNoAccount) {
		t.Fatalf("token-exhausted account was schedulable: %v", err)
	}

	now = reset.Add(time.Second)
	lease, err := pool.Acquire(context.Background(), Selection{Model: model})
	if err != nil {
		t.Fatal(err)
	}
	quota := domain.QuotaSnapshot{TokensLimit: 1000, TokensRemaining: 0, ObservedAt: now}
	if err := lease.Release(context.Background(), Feedback{StatusCode: 200, Quota: &quota}); err != nil {
		t.Fatal(err)
	}
	stored := store.Accounts["token-quota"]
	want := now.Add(time.Hour)
	if stored.Status != domain.AccountCooldown || stored.CooldownUntil == nil || !stored.CooldownUntil.Equal(want) {
		t.Fatalf("missing-reset token quota state = %+v, want cooldown until %v", stored, want)
	}
}

func TestPoolDoesNotRestartCooldownFromExpiredQuotaSnapshot(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	reset := now.Add(time.Minute)
	account := domain.Account{
		ID: "expired-quota", Kind: domain.CredentialCLIOAuth, Status: domain.AccountCooldown,
		ConcurrencyLimit: 1, HealthScore: 0.8, FailureCount: 1, CooldownUntil: &reset,
		Quota: domain.QuotaSnapshot{RequestsLimit: 10, RequestsRemaining: 0, ResetAt: &reset, ObservedAt: now},
	}
	store := NewMemoryStore([]domain.Account{account}, map[string]domain.Credentials{"expired-quota": {AccessToken: "token"}})
	pool := NewPool(store, DefaultPolicy())
	pool.now = func() time.Time { return now }
	model := domain.ModelSpec{ID: "grok", CredentialKinds: []domain.CredentialKind{domain.CredentialCLIOAuth}}
	if _, err := pool.Acquire(context.Background(), Selection{Model: model}); !errorsIs(err, ErrNoAccount) {
		t.Fatalf("account was schedulable before quota reset: %v", err)
	}

	now = reset.Add(time.Second)
	lease, err := pool.Acquire(context.Background(), Selection{Model: model})
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(context.Background(), Feedback{StatusCode: 200}); err != nil {
		t.Fatal(err)
	}
	stored := store.Accounts[account.ID]
	if stored.Status != domain.AccountActive || stored.CooldownUntil != nil {
		t.Fatalf("expired quota snapshot restarted cooldown: %+v", stored)
	}
	second, err := pool.Acquire(context.Background(), Selection{Model: model})
	if err != nil {
		t.Fatalf("account was not reusable after the expired quota reset: %v", err)
	}
	_ = second.Release(context.Background(), Feedback{StatusCode: 200})
}

func TestPool429UsesLaterRetryAfterThanQuotaReset(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	reset := now.Add(10 * time.Second)
	account := domain.Account{ID: "limited", Kind: domain.CredentialCLIOAuth, Status: domain.AccountActive, ConcurrencyLimit: 1, HealthScore: 1}
	store := NewMemoryStore([]domain.Account{account}, map[string]domain.Credentials{"limited": {AccessToken: "token"}})
	pool := NewPool(store, DefaultPolicy())
	pool.now = func() time.Time { return now }
	model := domain.ModelSpec{ID: "grok", CredentialKinds: []domain.CredentialKind{domain.CredentialCLIOAuth}}
	lease, err := pool.Acquire(context.Background(), Selection{Model: model})
	if err != nil {
		t.Fatal(err)
	}
	quota := domain.QuotaSnapshot{RequestsLimit: 10, RequestsRemaining: 0, ResetAt: &reset, ObservedAt: now}
	if err := lease.Release(context.Background(), Feedback{StatusCode: 429, RetryAfter: time.Minute, Quota: &quota}); err != nil {
		t.Fatal(err)
	}
	stored := store.Accounts[account.ID]
	want := now.Add(time.Minute)
	if stored.CooldownUntil == nil || !stored.CooldownUntil.Equal(want) {
		t.Fatalf("429 cooldown = %v, want later Retry-After %v", stored.CooldownUntil, want)
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

func TestPoolReloadRemovesDeletedAccountWithInflightLease(t *testing.T) {
	account := domain.Account{
		ID: "deleted", Kind: domain.CredentialCLIOAuth, Status: domain.AccountActive,
		ConcurrencyLimit: 2, HealthScore: 1,
	}
	store := NewMemoryStore([]domain.Account{account}, map[string]domain.Credentials{
		account.ID: {AccessToken: "token"},
	})
	pool := NewPool(store, DefaultPolicy())
	model := domain.ModelSpec{ID: "grok", CredentialKinds: []domain.CredentialKind{domain.CredentialCLIOAuth}}

	lease, err := pool.Acquire(context.Background(), Selection{Model: model})
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	delete(store.Accounts, account.ID)
	store.mu.Unlock()
	if err := pool.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Acquire(context.Background(), Selection{Model: model}); !errorsIs(err, ErrNoAccount) {
		t.Errorf("deleted account remained schedulable while an old lease was inflight: %v", err)
	}

	if err := lease.Release(context.Background(), Feedback{StatusCode: 200}); err != nil {
		t.Fatal(err)
	}
	store.mu.RLock()
	_, recreated := store.Accounts[account.ID]
	store.mu.RUnlock()
	if recreated {
		t.Fatal("releasing an old lease recreated the deleted account")
	}
	if _, err := pool.Acquire(context.Background(), Selection{Model: model}); !errorsIs(err, ErrNoAccount) {
		t.Fatalf("deleted account became schedulable after releasing its old lease: %v", err)
	}
}

func TestPoolReloadSerializesSnapshotsSoDeletedAccountStaysRemoved(t *testing.T) {
	account := domain.Account{
		ID: "deleted-during-reload", Kind: domain.CredentialCLIOAuth, Status: domain.AccountActive,
		ConcurrencyLimit: 1, HealthScore: 1,
	}
	memoryStore := NewMemoryStore([]domain.Account{account}, map[string]domain.Credentials{
		account.ID: {AccessToken: "token"},
	})
	store := &blockingReloadStore{
		MemoryStore:         memoryStore,
		firstSnapshotTaken:  make(chan struct{}),
		releaseFirst:        make(chan struct{}),
		secondSnapshotTaken: make(chan struct{}),
	}
	pool := NewPool(store, DefaultPolicy())

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- pool.Reload(context.Background())
	}()
	select {
	case <-store.firstSnapshotTaken:
	case <-time.After(time.Second):
		t.Fatal("first Reload did not capture its snapshot")
	}

	memoryStore.mu.Lock()
	delete(memoryStore.Accounts, account.ID)
	memoryStore.mu.Unlock()

	secondCalling := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		close(secondCalling)
		secondDone <- pool.Reload(context.Background())
	}()
	<-secondCalling

	secondReadBeforeRelease := false
	select {
	case <-store.secondSnapshotTaken:
		secondReadBeforeRelease = true
	case <-time.After(100 * time.Millisecond):
	}
	if secondReadBeforeRelease {
		// Make the stale first snapshot apply last if Reload permits overlapping
		// ListAccounts and reconciliation operations.
		if err := <-secondDone; err != nil {
			t.Fatalf("second Reload: %v", err)
		}
	}
	close(store.releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("first Reload: %v", err)
	}
	if !secondReadBeforeRelease {
		if err := <-secondDone; err != nil {
			t.Fatalf("second Reload: %v", err)
		}
	}
	if secondReadBeforeRelease {
		t.Error("second Reload read a new snapshot before the first Reload finished applying its snapshot")
	}

	model := domain.ModelSpec{ID: "grok", CredentialKinds: []domain.CredentialKind{domain.CredentialCLIOAuth}}
	if _, err := pool.Acquire(context.Background(), Selection{Model: model}); !errorsIs(err, ErrNoAccount) {
		t.Fatalf("an older Reload re-added the deleted account: %v", err)
	}
}

func TestPoolReloadWaitingForAnotherReloadHonorsContext(t *testing.T) {
	memoryStore := NewMemoryStore(nil, nil)
	store := &blockingReloadStore{
		MemoryStore:         memoryStore,
		firstSnapshotTaken:  make(chan struct{}),
		releaseFirst:        make(chan struct{}),
		secondSnapshotTaken: make(chan struct{}),
	}
	pool := NewPool(store, DefaultPolicy())
	firstDone := make(chan error, 1)
	go func() { firstDone <- pool.Reload(context.Background()) }()
	select {
	case <-store.firstSnapshotTaken:
	case <-time.After(time.Second):
		t.Fatal("first Reload did not start")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if err := pool.Reload(ctx); err != context.DeadlineExceeded {
		close(store.releaseFirst)
		t.Fatalf("queued Reload error = %v, want context deadline exceeded", err)
	}
	select {
	case <-store.secondSnapshotTaken:
		close(store.releaseFirst)
		t.Fatal("canceled Reload reached the store")
	default:
	}
	close(store.releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
}

func TestPoolSteadySuccessCoalescesLastUsedPersistence(t *testing.T) {
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
	if store.updates != 1 {
		t.Fatalf("steady success persisted %d account updates, want one coalesced last-used write", store.updates)
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

type blockingReloadStore struct {
	*MemoryStore

	callsMu             sync.Mutex
	calls               int
	firstSnapshotTaken  chan struct{}
	releaseFirst        chan struct{}
	secondSnapshotTaken chan struct{}
}

func (s *blockingReloadStore) ListAccounts(ctx context.Context) ([]domain.Account, error) {
	s.callsMu.Lock()
	s.calls++
	call := s.calls
	s.callsMu.Unlock()

	items, err := s.MemoryStore.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	switch call {
	case 1:
		close(s.firstSnapshotTaken)
		select {
		case <-s.releaseFirst:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	case 2:
		close(s.secondSnapshotTaken)
	}
	return items, nil
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
