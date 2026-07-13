package configsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/langrenjh-alt/GROK-GO/internal/accounts"
	"github.com/langrenjh-alt/GROK-GO/internal/config"
	"github.com/langrenjh-alt/GROK-GO/internal/configevent"
	"github.com/langrenjh-alt/GROK-GO/internal/database"
	"github.com/langrenjh-alt/GROK-GO/internal/runtimecfg"
)

func TestSynchronizerPropagatesReloadsIgnoresSelfAndReconnects(t *testing.T) {
	bus := newMemoryBus()
	shared := newSharedSettings(runtimecfg.Defaults().Map())
	shared.Set(map[string]any{"request_timeout_seconds": 31, "account_scheduling_strategy": "affinity"})
	storeA, storeB := &countingSettings{shared: shared}, &countingSettings{shared: shared}
	runtimeA := runtimecfg.MustRuntime(runtimecfg.Defaults(), runtimecfg.Defaults())
	runtimeB := runtimecfg.MustRuntime(runtimecfg.Defaults(), runtimecfg.Defaults())
	accountsA, accountsB := &strategyTarget{}, &strategyTarget{}
	syncA := mustSynchronizer(t, Config{Bus: bus, Settings: storeA, RuntimeSettings: runtimeA, Accounts: accountsA, InstanceID: "instance-a", ReconnectMin: 5 * time.Millisecond, ReconnectMax: 20 * time.Millisecond})
	syncB := mustSynchronizer(t, Config{Bus: bus, Settings: storeB, RuntimeSettings: runtimeB, Accounts: accountsB, InstanceID: "instance-b", ReconnectMin: 5 * time.Millisecond, ReconnectMax: 20 * time.Millisecond})

	ctx, cancel := context.WithCancel(context.Background())
	doneA, doneB := runSynchronizer(ctx, syncA), runSynchronizer(ctx, syncB)
	t.Cleanup(func() {
		cancel()
		waitRun(t, doneA)
		waitRun(t, doneB)
	})
	waitFor(t, func() bool {
		return bus.SubscriberCount() == 2 && runtimeA.Active().RequestTimeoutSeconds == 31 && runtimeB.Active().RequestTimeoutSeconds == 31 && accountsA.Reloads() > 0 && accountsB.Reloads() > 0
	})
	storeA.loads.Store(0)
	storeB.loads.Store(0)
	accountsA.reloads.Store(0)
	accountsB.reloads.Store(0)

	shared.Set(map[string]any{"request_timeout_seconds": 45, "max_concurrency": 77})
	configured, err := runtimecfg.Resolve(runtimeA.Defaults(), shared.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	runtimeA.Apply(configured)
	if err := syncA.Notify(ctx, configevent.ScopeRuntimeSettings); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		active := runtimeB.Active()
		return active.RequestTimeoutSeconds == 45 && active.MaxConcurrency == 77
	})
	time.Sleep(10 * time.Millisecond)
	if got := storeA.loads.Load(); got != 0 {
		t.Fatalf("origin instance reloaded its own runtime notification %d times", got)
	}
	if got := storeB.loads.Load(); got != 1 {
		t.Fatalf("peer runtime reload count = %d, want 1", got)
	}
	if got := accountsB.Reloads(); got != 0 {
		t.Fatalf("runtime notification reloaded accounts %d times", got)
	}

	shared.Set(map[string]any{"account_scheduling_strategy": "round_robin"})
	if err := accountsA.SetStrategy(accounts.StrategyRoundRobin); err != nil {
		t.Fatal(err)
	}
	if err := syncA.Notify(ctx, configevent.ScopeAccountStrategy); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return accountsB.Strategy() == accounts.StrategyRoundRobin })
	if got := storeA.loads.Load(); got != 0 {
		t.Fatalf("origin instance reloaded its own strategy notification %d times", got)
	}

	storeA.loads.Store(0)
	storeB.loads.Store(0)
	accountsA.reloads.Store(0)
	accountsB.reloads.Store(0)
	if err := syncA.Notify(ctx, configevent.ScopeAccounts); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return accountsB.Reloads() == 1 })
	if got := accountsA.Reloads(); got != 0 {
		t.Fatalf("origin instance reloaded its own account notification %d times", got)
	}
	if got := storeB.loads.Load(); got != 0 {
		t.Fatalf("account notification loaded runtime settings %d times", got)
	}

	bus.Disconnect()
	waitFor(t, func() bool { return bus.SubscriberCount() == 0 })
	accountsA.reloads.Store(0)
	accountsB.reloads.Store(0)
	shared.Set(map[string]any{"request_timeout_seconds": 60, "account_scheduling_strategy": "priority"})
	if err := syncA.Notify(ctx, configevent.ScopeRuntimeSettings); err != nil {
		t.Fatal(err)
	}
	bus.Reconnect()
	waitFor(t, func() bool {
		return bus.SubscribeCalls() >= 4 && runtimeB.Active().RequestTimeoutSeconds == 60 && accountsB.Strategy() == accounts.StrategyPriority && accountsA.Reloads() > 0 && accountsB.Reloads() > 0
	})

}

func TestSynchronizerRejectsUnknownNotificationScope(t *testing.T) {
	synchronizer := mustSynchronizer(t, Config{
		Bus: newMemoryBus(), Settings: newSharedSettings(runtimecfg.Defaults().Map()),
		RuntimeSettings: runtimecfg.MustRuntime(runtimecfg.Defaults(), runtimecfg.Defaults()),
		Accounts:        &strategyTarget{}, InstanceID: "instance",
	})
	if err := synchronizer.Notify(context.Background(), "unknown"); err == nil {
		t.Fatal("unknown notification scope was accepted")
	}
}

func TestSynchronizerKeepsCurrentStrategyWhenPersistedValueIsInvalid(t *testing.T) {
	settings := newSharedSettings(runtimecfg.Defaults().Map())
	settings.Set(map[string]any{"account_scheduling_strategy": "invalid"})
	target := &strategyTarget{}
	if err := target.SetStrategy(accounts.StrategyRoundRobin); err != nil {
		t.Fatal(err)
	}
	synchronizer := mustSynchronizer(t, Config{
		Bus: newMemoryBus(), Settings: settings,
		RuntimeSettings: runtimecfg.MustRuntime(runtimecfg.Defaults(), runtimecfg.Defaults()),
		Accounts:        target, InstanceID: "instance",
	})
	if err := synchronizer.reload(context.Background(), configevent.ScopeAccountStrategy); err == nil {
		t.Fatal("invalid persisted strategy did not return an error")
	}
	if got := target.Strategy(); got != accounts.StrategyRoundRobin {
		t.Fatalf("strategy changed to %q after invalid reload", got)
	}
}

func TestSynchronizerWithRedisFixture(t *testing.T) {
	redisURL := os.Getenv("GROK_GO_TEST_REDIS_URL")
	if redisURL == "" {
		t.Skip("integration Redis URL is not configured")
	}
	ctx, cancel := context.WithCancel(context.Background())
	redis, err := database.OpenRedis(ctx, config.RedisConfig{
		URL: redisURL, KeyPrefix: fmt.Sprintf("grok-go:configsync-test:%d:", time.Now().UnixNano()),
		DialTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, HealthTimeout: 5 * time.Second,
	})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = redis.Close() })
	shared := newSharedSettings(runtimecfg.Defaults().Map())
	shared.Set(map[string]any{"request_timeout_seconds": 52})
	runtimeA := runtimecfg.MustRuntime(runtimecfg.Defaults(), runtimecfg.Defaults())
	runtimeB := runtimecfg.MustRuntime(runtimecfg.Defaults(), runtimecfg.Defaults())
	syncA := mustSynchronizer(t, Config{Bus: redis, Settings: shared, RuntimeSettings: runtimeA, Accounts: &strategyTarget{}, InstanceID: "redis-a"})
	syncB := mustSynchronizer(t, Config{Bus: redis, Settings: shared, RuntimeSettings: runtimeB, Accounts: &strategyTarget{}, InstanceID: "redis-b"})
	doneA, doneB := runSynchronizer(ctx, syncA), runSynchronizer(ctx, syncB)
	t.Cleanup(func() {
		cancel()
		waitRun(t, doneA)
		waitRun(t, doneB)
	})

	waitFor(t, func() bool {
		return runtimeA.Active().RequestTimeoutSeconds == 52 && runtimeB.Active().RequestTimeoutSeconds == 52
	})
	shared.Set(map[string]any{"request_timeout_seconds": 53})
	configured, err := runtimecfg.Resolve(runtimeA.Defaults(), shared.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	runtimeA.Apply(configured)
	if err := syncA.Notify(ctx, configevent.ScopeRuntimeSettings); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return runtimeB.Active().RequestTimeoutSeconds == 53 })
}

type sharedSettings struct {
	mu     sync.RWMutex
	values map[string]any
}

func newSharedSettings(values map[string]any) *sharedSettings {
	s := &sharedSettings{}
	s.Set(values)
	return s
}

func (s *sharedSettings) Set(values map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.values == nil {
		s.values = make(map[string]any)
	}
	for key, value := range values {
		s.values[key] = value
	}
}

func (s *sharedSettings) Snapshot() map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	encoded, _ := json.Marshal(s.values)
	var result map[string]any
	_ = json.Unmarshal(encoded, &result)
	return result
}

func (s *sharedSettings) LoadSettings(context.Context) (map[string]any, error) {
	return s.Snapshot(), nil
}

type countingSettings struct {
	shared *sharedSettings
	loads  atomic.Int64
}

func (s *countingSettings) LoadSettings(ctx context.Context) (map[string]any, error) {
	value, err := s.shared.LoadSettings(ctx)
	if err == nil {
		s.loads.Add(1)
	}
	return value, err
}

type strategyTarget struct {
	mu       sync.Mutex
	strategy accounts.Strategy
	reloads  atomic.Int64
}

func (t *strategyTarget) SetStrategy(strategy accounts.Strategy) error {
	parsed, err := accounts.ParseStrategy(string(strategy))
	if err != nil {
		return err
	}
	t.mu.Lock()
	t.strategy = parsed
	t.mu.Unlock()
	return nil
}

func (t *strategyTarget) Strategy() accounts.Strategy {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.strategy
}

func (t *strategyTarget) Reload(context.Context) error {
	t.reloads.Add(1)
	return nil
}

func (t *strategyTarget) Reloads() int64 {
	return t.reloads.Load()
}

type memoryBus struct {
	mu             sync.Mutex
	available      bool
	nextID         int
	subscribeCalls int
	subscriptions  map[int]*memorySubscription
}

func newMemoryBus() *memoryBus {
	return &memoryBus{available: true, subscriptions: make(map[int]*memorySubscription)}
}

func (b *memoryBus) Publish(_ context.Context, _ string, payload []byte) error {
	b.mu.Lock()
	if !b.available {
		b.mu.Unlock()
		return nil
	}
	subscriptions := make([]*memorySubscription, 0, len(b.subscriptions))
	for _, subscription := range b.subscriptions {
		subscriptions = append(subscriptions, subscription)
	}
	b.mu.Unlock()
	for _, subscription := range subscriptions {
		select {
		case subscription.messages <- append([]byte(nil), payload...):
		default:
		}
	}
	return nil
}

func (b *memoryBus) Subscribe(_ context.Context, _ string) (configevent.Subscription, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subscribeCalls++
	if !b.available {
		return nil, errors.New("memory bus is disconnected")
	}
	b.nextID++
	subscription := &memorySubscription{id: b.nextID, bus: b, messages: make(chan []byte, 16), failures: make(chan error, 1)}
	b.subscriptions[subscription.id] = subscription
	return subscription, nil
}

func (b *memoryBus) Disconnect() {
	b.mu.Lock()
	b.available = false
	subscriptions := make([]*memorySubscription, 0, len(b.subscriptions))
	for _, subscription := range b.subscriptions {
		subscriptions = append(subscriptions, subscription)
	}
	b.mu.Unlock()
	for _, subscription := range subscriptions {
		select {
		case subscription.failures <- errors.New("memory bus connection lost"):
		default:
		}
	}
}

func (b *memoryBus) Reconnect() {
	b.mu.Lock()
	b.available = true
	b.mu.Unlock()
}

func (b *memoryBus) SubscriberCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subscriptions)
}

func (b *memoryBus) SubscribeCalls() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.subscribeCalls
}

type memorySubscription struct {
	id       int
	bus      *memoryBus
	messages chan []byte
	failures chan error
	close    sync.Once
}

func (s *memorySubscription) Receive(ctx context.Context) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case err := <-s.failures:
		return nil, err
	case payload := <-s.messages:
		return payload, nil
	}
}

func (s *memorySubscription) Close() error {
	s.close.Do(func() {
		s.bus.mu.Lock()
		delete(s.bus.subscriptions, s.id)
		s.bus.mu.Unlock()
	})
	return nil
}

func mustSynchronizer(t *testing.T, config Config) *Synchronizer {
	t.Helper()
	synchronizer, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	return synchronizer
}

func runSynchronizer(ctx context.Context, synchronizer *Synchronizer) <-chan error {
	done := make(chan error, 1)
	go func() { done <- synchronizer.Run(ctx) }()
	return done
}

func waitRun(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("configuration synchronizer did not stop")
	}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition was not satisfied before timeout")
}
