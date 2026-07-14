package accounts

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/langrenjh-alt/GROK-GO/internal/database"
	"github.com/langrenjh-alt/GROK-GO/internal/domain"
)

func TestRedisCoordinatorLeaseOwnershipAndTTL(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	redis := newFakeRedis(func() time.Time { return now })
	first := NewRedisCoordinator(redis)
	second := NewRedisCoordinator(redis)

	leaseA, acquired, err := first.AcquireLease(context.Background(), "account-a", 2, time.Minute)
	if err != nil || !acquired {
		t.Fatalf("acquire first lease: acquired=%v err=%v", acquired, err)
	}
	leaseB, acquired, err := second.AcquireLease(context.Background(), "account-a", 2, time.Minute)
	if err != nil || !acquired {
		t.Fatalf("acquire second lease: acquired=%v err=%v", acquired, err)
	}
	if _, acquired, err := first.AcquireLease(context.Background(), "account-a", 2, time.Minute); err != nil || acquired {
		t.Fatalf("expected shared concurrency exhaustion: acquired=%v err=%v", acquired, err)
	}

	if err := first.ReleaseLease(context.Background(), leaseA); err != nil {
		t.Fatal(err)
	}
	leaseC, acquired, err := second.AcquireLease(context.Background(), "account-a", 2, time.Minute)
	if err != nil || !acquired {
		t.Fatalf("reacquire released slot: acquired=%v err=%v", acquired, err)
	}
	if leaseC.Slot != leaseA.Slot {
		t.Fatalf("expected slot %d to be reused, got %d", leaseA.Slot, leaseC.Slot)
	}

	// A repeated release from the stale owner must not delete leaseC.
	if err := first.ReleaseLease(context.Background(), leaseA); err != nil {
		t.Fatal(err)
	}
	if _, acquired, err := first.AcquireLease(context.Background(), "account-a", 2, time.Minute); err != nil || acquired {
		t.Fatalf("stale release removed a current lease: acquired=%v err=%v", acquired, err)
	}

	now = now.Add(2 * time.Minute)
	if _, acquired, err := first.AcquireLease(context.Background(), "account-a", 2, time.Minute); err != nil || !acquired {
		t.Fatalf("expired leases should free capacity: acquired=%v err=%v", acquired, err)
	}
	_ = leaseB
}

func TestRedisCoordinatorAffinityAndCooldownTTL(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	redis := newFakeRedis(func() time.Time { return now })
	coordinator := NewRedisCoordinator(redis)
	coordinator.now = func() time.Time { return now }
	ctx := context.Background()

	if _, found, err := coordinator.GetAffinity(ctx, "grok", "thread"); err != nil || found {
		t.Fatalf("unexpected initial affinity: found=%v err=%v", found, err)
	}
	if accountID, err := coordinator.BindAffinity(ctx, "grok", "thread", "account-a", time.Minute); err != nil || accountID != "account-a" {
		t.Fatalf("bind first affinity: account=%q err=%v", accountID, err)
	}
	if accountID, err := coordinator.BindAffinity(ctx, "grok", "thread", "account-b", time.Minute); err != nil || accountID != "account-a" {
		t.Fatalf("existing affinity should win: account=%q err=%v", accountID, err)
	}

	until := now.Add(45 * time.Second)
	if err := coordinator.SetCooldown(ctx, "account-a", until); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.SetCooldown(ctx, "account-a", now.Add(10*time.Second)); err != nil {
		t.Fatal(err)
	}
	got, cooling, err := coordinator.CooldownUntil(ctx, "account-a")
	if err != nil || !cooling || !got.Equal(until) {
		t.Fatalf("cooldown mismatch: got=%v cooling=%v err=%v", got, cooling, err)
	}

	now = now.Add(2 * time.Minute)
	if accountID, err := coordinator.BindAffinity(ctx, "grok", "thread", "account-b", time.Minute); err != nil || accountID != "account-b" {
		t.Fatalf("expired affinity was not replaced: account=%q err=%v", accountID, err)
	}
	if _, cooling, err := coordinator.CooldownUntil(ctx, "account-a"); err != nil || cooling {
		t.Fatalf("expired cooldown remained active: cooling=%v err=%v", cooling, err)
	}
}

func TestPoolCoordinatorEnforcesCrossInstanceLeaseAndCooldown(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	redis := newFakeRedis(func() time.Time { return now })
	coordinatorA := NewRedisCoordinator(redis)
	coordinatorB := NewRedisCoordinator(redis)
	coordinatorA.now = func() time.Time { return now }
	coordinatorB.now = func() time.Time { return now }

	account := domain.Account{ID: "account-a", Kind: domain.CredentialCLIOAuth, Status: domain.AccountActive, ConcurrencyLimit: 1}
	store := NewMemoryStore([]domain.Account{account}, map[string]domain.Credentials{"account-a": {AccessToken: "token"}})
	poolA := NewPoolWithCoordinator(store, DefaultPolicy(), coordinatorA)
	poolB := NewPoolWithCoordinator(store, DefaultPolicy(), coordinatorB)
	poolA.now = func() time.Time { return now }
	poolB.now = func() time.Time { return now }
	selection := Selection{Model: domain.ModelSpec{ID: "grok", CredentialKinds: []domain.CredentialKind{domain.CredentialCLIOAuth}}}

	lease, err := poolA.Acquire(context.Background(), selection)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := poolB.Acquire(context.Background(), selection); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("second pool bypassed shared concurrency: %v", err)
	}
	if err := lease.Release(context.Background(), Feedback{StatusCode: 429, RetryAfter: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if _, err := poolB.Acquire(context.Background(), selection); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("second pool bypassed shared cooldown: %v", err)
	}

	now = now.Add(2 * time.Minute)
	if err := poolB.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := poolB.Acquire(context.Background(), selection)
	if err != nil {
		t.Fatalf("acquire after cooldown: %v", err)
	}
	if err := got.Release(context.Background(), Feedback{StatusCode: 200}); err != nil {
		t.Fatal(err)
	}
}

func TestPoolReloadDoesNotClearCoordinatedCooldown(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	redis := newFakeRedis(func() time.Time { return now })
	coordinator := NewRedisCoordinator(redis)
	coordinator.now = func() time.Time { return now }
	account := domain.Account{
		ID: "account-a", Kind: domain.CredentialCLIOAuth, Status: domain.AccountActive,
		ConcurrencyLimit: 1, CredentialCipher: []byte("sealed-credential"),
	}
	store := NewMemoryStore([]domain.Account{account}, map[string]domain.Credentials{"account-a": {AccessToken: "token"}})
	pool := NewPoolWithCoordinator(store, DefaultPolicy(), coordinator)
	pool.now = func() time.Time { return now }
	until := now.Add(time.Minute)
	coordinationID := account.ID
	if err := coordinator.SetCooldown(context.Background(), coordinationID, until); err != nil {
		t.Fatal(err)
	}
	if err := pool.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, cooling, err := coordinator.CooldownUntil(context.Background(), coordinationID); err != nil || !cooling || !got.Equal(until) {
		t.Fatalf("reload cleared coordinated cooldown: got=%v cooling=%v err=%v", got, cooling, err)
	}
	if err := pool.ClearCooldown(context.Background(), account.ID); err != nil {
		t.Fatal(err)
	}
	if _, cooling, err := coordinator.CooldownUntil(context.Background(), coordinationID); err != nil || cooling {
		t.Fatalf("explicit activation did not clear cooldown: cooling=%v err=%v", cooling, err)
	}
}

func TestPoolCoordinatorKeepsCrossInstanceAffinityUntilTTL(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	redis := newFakeRedis(func() time.Time { return now })
	coordinatorA := NewRedisCoordinator(redis)
	coordinatorB := NewRedisCoordinator(redis)
	coordinatorA.now = func() time.Time { return now }
	coordinatorB.now = func() time.Time { return now }
	policy := DefaultPolicy()
	policy.AffinityTTL = time.Minute
	model := domain.ModelSpec{ID: "grok", CredentialKinds: []domain.CredentialKind{domain.CredentialCLIOAuth}}
	credentials := map[string]domain.Credentials{"account-a": {AccessToken: "a"}, "account-b": {AccessToken: "b"}}
	poolA := NewPoolWithCoordinator(NewMemoryStore([]domain.Account{
		{ID: "account-a", Kind: domain.CredentialCLIOAuth, Status: domain.AccountActive, Priority: 2},
		{ID: "account-b", Kind: domain.CredentialCLIOAuth, Status: domain.AccountActive, Priority: 1},
	}, credentials), policy, coordinatorA)
	poolB := NewPoolWithCoordinator(NewMemoryStore([]domain.Account{
		{ID: "account-a", Kind: domain.CredentialCLIOAuth, Status: domain.AccountActive, Priority: 1},
		{ID: "account-b", Kind: domain.CredentialCLIOAuth, Status: domain.AccountActive, Priority: 2},
	}, credentials), policy, coordinatorB)
	poolA.now = func() time.Time { return now }
	poolB.now = func() time.Time { return now }
	selection := Selection{Model: model, AffinityKey: "thread"}

	leaseA, err := poolA.Acquire(context.Background(), selection)
	if err != nil {
		t.Fatal(err)
	}
	if leaseA.Account.ID != "account-a" {
		t.Fatalf("first pool should bind account-a, got %s", leaseA.Account.ID)
	}
	if err := leaseA.Release(context.Background(), Feedback{StatusCode: 200}); err != nil {
		t.Fatal(err)
	}
	leaseB, err := poolB.Acquire(context.Background(), selection)
	if err != nil {
		t.Fatal(err)
	}
	if leaseB.Account.ID != "account-a" {
		t.Fatalf("second pool ignored coordinated affinity, got %s", leaseB.Account.ID)
	}
	if err := leaseB.Release(context.Background(), Feedback{StatusCode: 200}); err != nil {
		t.Fatal(err)
	}

	now = now.Add(45 * time.Second)
	refreshing, err := poolB.Acquire(context.Background(), selection)
	if err != nil || refreshing.Account.ID != "account-a" {
		t.Fatalf("active affinity was not refreshed: lease=%+v err=%v", refreshing, err)
	}
	if err := refreshing.Release(context.Background(), Feedback{StatusCode: 200}); err != nil {
		t.Fatal(err)
	}

	now = now.Add(30 * time.Second)
	stillBound, err := poolB.Acquire(context.Background(), selection)
	if err != nil || stillBound.Account.ID != "account-a" {
		t.Fatalf("refreshed affinity expired from its original deadline: lease=%+v err=%v", stillBound, err)
	}
	if err := stillBound.Release(context.Background(), Feedback{StatusCode: 200}); err != nil {
		t.Fatal(err)
	}

	now = now.Add(2 * time.Minute)
	afterTTL, err := poolB.Acquire(context.Background(), selection)
	if err != nil {
		t.Fatal(err)
	}
	if afterTTL.Account.ID != "account-b" {
		t.Fatalf("expired Redis affinity should be recalculated, got %s", afterTTL.Account.ID)
	}
	if err := afterTTL.Release(context.Background(), Feedback{StatusCode: 200}); err != nil {
		t.Fatal(err)
	}
}

func TestPoolCoordinatorFailsClosedAndRollsBackLocalReservation(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	redis := newFakeRedis(func() time.Time { return now })
	coordinator := NewRedisCoordinator(redis)
	coordinator.now = func() time.Time { return now }
	account := domain.Account{ID: "account-a", Kind: domain.CredentialGrokSSO, Status: domain.AccountActive, ConcurrencyLimit: 1}
	store := NewMemoryStore([]domain.Account{account}, map[string]domain.Credentials{"account-a": {SSO: "cookie"}})
	pool := NewPoolWithCoordinator(store, DefaultPolicy(), coordinator)
	pool.now = func() time.Time { return now }
	selection := Selection{Model: domain.ModelSpec{ID: "grok", CredentialKinds: []domain.CredentialKind{domain.CredentialGrokSSO}}}

	redis.setNXErr = errors.New("redis unavailable")
	if _, err := pool.Acquire(context.Background(), selection); err == nil || errors.Is(err, ErrNoAccount) {
		t.Fatalf("coordinator failure should be returned, got %v", err)
	}
	redis.setNXErr = nil
	lease, err := pool.Acquire(context.Background(), selection)
	if err != nil {
		t.Fatalf("local reservation leaked after coordinator error: %v", err)
	}
	if err := lease.Release(context.Background(), Feedback{StatusCode: 200}); err != nil {
		t.Fatal(err)
	}
}

type fakeRedisEntry struct {
	value     []byte
	expiresAt time.Time
}

type fakeRedis struct {
	mu       sync.Mutex
	now      func() time.Time
	values   map[string]fakeRedisEntry
	leases   map[string]map[string]time.Time
	setNXErr error
}

func newFakeRedis(now func() time.Time) *fakeRedis {
	return &fakeRedis{now: now, values: make(map[string]fakeRedisEntry), leases: make(map[string]map[string]time.Time)}
}

func (r *fakeRedis) Get(_ context.Context, key string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.getLocked(key)
	if !ok {
		return nil, database.ErrCacheMiss
	}
	return bytes.Clone(entry.value), nil
}

func (r *fakeRedis) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.values[key] = fakeRedisEntry{value: bytes.Clone(value), expiresAt: r.now().Add(ttl)}
	return nil
}

func (r *fakeRedis) SetNX(_ context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.setNXErr != nil {
		return false, r.setNXErr
	}
	if _, ok := r.getLocked(key); ok {
		return false, nil
	}
	r.values[key] = fakeRedisEntry{value: bytes.Clone(value), expiresAt: r.now().Add(ttl)}
	return true, nil
}

func (r *fakeRedis) GetDelete(_ context.Context, key string) ([]byte, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.getLocked(key)
	if !ok {
		return nil, false, nil
	}
	delete(r.values, key)
	return bytes.Clone(entry.value), true, nil
}

func (r *fakeRedis) AcquireSlot(_ context.Context, key string, limit int, ttl time.Duration) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.getLocked(key)
	current := 0
	if ok {
		current, _ = strconv.Atoi(string(entry.value))
	}
	if current >= limit {
		return false, nil
	}
	r.values[key] = fakeRedisEntry{value: []byte(strconv.Itoa(current + 1)), expiresAt: r.now().Add(ttl)}
	return true, nil
}

func (r *fakeRedis) ReleaseSlot(_ context.Context, key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.getLocked(key)
	if !ok {
		return nil
	}
	current, _ := strconv.Atoi(string(entry.value))
	if current <= 1 {
		delete(r.values, key)
		return nil
	}
	entry.value = []byte(strconv.Itoa(current - 1))
	r.values[key] = entry
	return nil
}

func (r *fakeRedis) CompareDelete(_ context.Context, key string, expected []byte) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.getLocked(key)
	if !ok || !bytes.Equal(entry.value, expected) {
		return false, nil
	}
	delete(r.values, key)
	return true, nil
}

func (r *fakeRedis) CompareExpire(_ context.Context, key string, expected []byte, ttl time.Duration) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.getLocked(key)
	if !ok || !bytes.Equal(entry.value, expected) {
		return false, nil
	}
	entry.expiresAt = r.now().Add(ttl)
	r.values[key] = entry
	return true, nil
}

func (r *fakeRedis) SetIfGreater(_ context.Context, key string, value int64, expiresAt time.Time) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if entry, ok := r.getLocked(key); ok {
		current, err := strconv.ParseInt(string(entry.value), 10, 64)
		if err == nil && current >= value {
			return false, nil
		}
	}
	r.values[key] = fakeRedisEntry{value: []byte(strconv.FormatInt(value, 10)), expiresAt: expiresAt}
	return true, nil
}

func (r *fakeRedis) AcquireLeaseSlot(_ context.Context, key, owner string, limit int, ttl time.Duration) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.setNXErr != nil {
		return false, r.setNXErr
	}
	owners := r.leases[key]
	if owners == nil {
		owners = make(map[string]time.Time)
		r.leases[key] = owners
	}
	for candidate, expiresAt := range owners {
		if !expiresAt.After(r.now()) {
			delete(owners, candidate)
		}
	}
	if _, exists := owners[owner]; exists {
		owners[owner] = r.now().Add(ttl)
		return true, nil
	}
	if len(owners) >= limit {
		return false, nil
	}
	owners[owner] = r.now().Add(ttl)
	return true, nil
}

func (r *fakeRedis) ReleaseLeaseSlot(_ context.Context, key, owner string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.leases[key], owner)
	if len(r.leases[key]) == 0 {
		delete(r.leases, key)
	}
	return nil
}

func (r *fakeRedis) getLocked(key string) (fakeRedisEntry, bool) {
	entry, ok := r.values[key]
	if !ok {
		return fakeRedisEntry{}, false
	}
	if !entry.expiresAt.After(r.now()) {
		delete(r.values, key)
		return fakeRedisEntry{}, false
	}
	return entry, true
}
