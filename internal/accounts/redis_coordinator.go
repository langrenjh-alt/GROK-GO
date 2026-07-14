package accounts

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/langrenjh-alt/GROK-GO/internal/database"
)

const defaultCoordinatorNamespace = "account-pool"

// Coordinator keeps account scheduling state consistent across processes.
type Coordinator interface {
	AcquireLease(context.Context, string, int, time.Duration) (CoordinationLease, bool, error)
	ReleaseLease(context.Context, CoordinationLease) error
	GetAffinity(context.Context, string, string) (string, bool, error)
	BindAffinity(context.Context, string, string, string, time.Duration) (string, bool, error)
	ClearAffinity(context.Context, string, string, string) error
	CooldownUntil(context.Context, string) (time.Time, bool, error)
	SetCooldown(context.Context, string, time.Time) error
	ClearCooldown(context.Context, string) error
}

// CoordinationLease identifies one Redis-owned concurrency slot.
type CoordinationLease struct {
	AccountID string
	Slot      int
	Owner     string
}

// RedisCommands is implemented by database.Redis. The complete common command
// surface is retained here so alternate Redis adapters can be injected without
// exposing a concrete client to the account pool.
type RedisCommands interface {
	Get(context.Context, string) ([]byte, error)
	Set(context.Context, string, []byte, time.Duration) error
	SetNX(context.Context, string, []byte, time.Duration) (bool, error)
	GetDelete(context.Context, string) ([]byte, bool, error)
	AcquireSlot(context.Context, string, int, time.Duration) (bool, error)
	ReleaseSlot(context.Context, string) error
	CompareDelete(context.Context, string, []byte) (bool, error)
	CompareExpire(context.Context, string, []byte, time.Duration) (bool, error)
	SetIfGreater(context.Context, string, int64, time.Time) (bool, error)
	AcquireLeaseSlot(context.Context, string, string, int, time.Duration) (bool, error)
	ReleaseLeaseSlot(context.Context, string, string) error
}

// RedisCoordinator uses owner-specific members in one atomic sorted set per
// account. Release remains idempotent without a per-limit Redis round trip.
type RedisCoordinator struct {
	redis     RedisCommands
	namespace string
	now       func() time.Time
	random    func([]byte) (int, error)
}

var (
	_ RedisCommands = (*database.Redis)(nil)
	_ Coordinator   = (*RedisCoordinator)(nil)
)

func NewRedisCoordinator(redis RedisCommands) *RedisCoordinator {
	return NewRedisCoordinatorWithNamespace(redis, defaultCoordinatorNamespace)
}

func NewRedisCoordinatorWithNamespace(redis RedisCommands, namespace string) *RedisCoordinator {
	namespace = strings.Trim(namespace, ":")
	if namespace == "" {
		namespace = defaultCoordinatorNamespace
	}
	return &RedisCoordinator{
		redis:     redis,
		namespace: namespace,
		now:       time.Now,
		random:    rand.Read,
	}
}

func (c *RedisCoordinator) AcquireLease(ctx context.Context, accountID string, limit int, ttl time.Duration) (CoordinationLease, bool, error) {
	if c == nil || c.redis == nil {
		return CoordinationLease{}, false, errors.New("account coordinator is not configured")
	}
	if accountID == "" {
		return CoordinationLease{}, false, errors.New("account lease requires an account ID")
	}
	if limit <= 0 {
		return CoordinationLease{}, false, errors.New("account lease limit must be positive")
	}
	if ttl <= 0 {
		return CoordinationLease{}, false, errors.New("account lease TTL must be positive")
	}

	ownerBytes := make([]byte, 16)
	if _, err := c.random(ownerBytes); err != nil {
		return CoordinationLease{}, false, fmt.Errorf("create account lease owner: %w", err)
	}
	owner := hex.EncodeToString(ownerBytes)
	acquired, err := c.redis.AcquireLeaseSlot(ctx, c.leaseKey(accountID), owner, limit, ttl)
	if err != nil {
		return CoordinationLease{}, false, fmt.Errorf("acquire account lease: %w", err)
	}
	if !acquired {
		return CoordinationLease{}, false, nil
	}
	return CoordinationLease{AccountID: accountID, Slot: 0, Owner: owner}, true, nil
}

func (c *RedisCoordinator) ReleaseLease(ctx context.Context, lease CoordinationLease) error {
	if c == nil || c.redis == nil {
		return errors.New("account coordinator is not configured")
	}
	if lease.AccountID == "" || lease.Slot < 0 || lease.Owner == "" {
		return errors.New("invalid account coordination lease")
	}
	if err := c.redis.ReleaseLeaseSlot(ctx, c.leaseKey(lease.AccountID), lease.Owner); err != nil {
		return fmt.Errorf("release account lease: %w", err)
	}
	return nil
}

func (c *RedisCoordinator) GetAffinity(ctx context.Context, model, affinity string) (string, bool, error) {
	if c == nil || c.redis == nil {
		return "", false, errors.New("account coordinator is not configured")
	}
	value, err := c.redis.Get(ctx, c.affinityKey(model, affinity))
	if errors.Is(err, database.ErrCacheMiss) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read account affinity: %w", err)
	}
	if len(value) == 0 {
		return "", false, errors.New("account affinity is empty")
	}
	return string(value), true, nil
}

// BindAffinity atomically creates a mapping and returns the winning account ID
// when another process created the same mapping concurrently.
func (c *RedisCoordinator) BindAffinity(ctx context.Context, model, affinity, accountID string, ttl time.Duration) (string, bool, error) {
	if c == nil || c.redis == nil {
		return "", false, errors.New("account coordinator is not configured")
	}
	if accountID == "" {
		return "", false, errors.New("account affinity requires an account ID")
	}
	if ttl <= 0 {
		return "", false, errors.New("account affinity TTL must be positive")
	}
	key := c.affinityKey(model, affinity)
	for attempt := 0; attempt < 3; attempt++ {
		created, err := c.redis.SetNX(ctx, key, []byte(accountID), ttl)
		if err != nil {
			return "", false, fmt.Errorf("bind account affinity: %w", err)
		}
		if created {
			return accountID, true, nil
		}
		value, err := c.redis.Get(ctx, key)
		if errors.Is(err, database.ErrCacheMiss) {
			continue
		}
		if err != nil {
			return "", false, fmt.Errorf("read bound account affinity: %w", err)
		}
		if len(value) == 0 {
			return "", false, errors.New("bound account affinity is empty")
		}
		refreshed, err := c.redis.CompareExpire(ctx, key, value, ttl)
		if err != nil {
			return "", false, fmt.Errorf("refresh bound account affinity: %w", err)
		}
		if !refreshed {
			continue
		}
		return string(value), false, nil
	}
	return "", false, errors.New("account affinity changed while binding")
}

func (c *RedisCoordinator) ClearAffinity(ctx context.Context, model, affinity, accountID string) error {
	if c == nil || c.redis == nil {
		return errors.New("account coordinator is not configured")
	}
	if accountID == "" {
		return errors.New("account affinity requires an account ID")
	}
	if _, err := c.redis.CompareDelete(ctx, c.affinityKey(model, affinity), []byte(accountID)); err != nil {
		return fmt.Errorf("clear account affinity: %w", err)
	}
	return nil
}

func (c *RedisCoordinator) CooldownUntil(ctx context.Context, accountID string) (time.Time, bool, error) {
	if c == nil || c.redis == nil {
		return time.Time{}, false, errors.New("account coordinator is not configured")
	}
	value, err := c.redis.Get(ctx, c.cooldownKey(accountID))
	if errors.Is(err, database.ErrCacheMiss) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("read account cooldown: %w", err)
	}
	nanos, err := strconv.ParseInt(string(value), 10, 64)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("parse account cooldown: %w", err)
	}
	until := time.Unix(0, nanos)
	if !until.After(c.now()) {
		return time.Time{}, false, nil
	}
	return until, true, nil
}

func (c *RedisCoordinator) SetCooldown(ctx context.Context, accountID string, until time.Time) error {
	if c == nil || c.redis == nil {
		return errors.New("account coordinator is not configured")
	}
	if accountID == "" {
		return errors.New("account cooldown requires an account ID")
	}
	ttl := until.Sub(c.now())
	if ttl <= 0 {
		return nil
	}
	if _, err := c.redis.SetIfGreater(ctx, c.cooldownKey(accountID), until.UnixNano(), until); err != nil {
		return fmt.Errorf("set account cooldown: %w", err)
	}
	return nil
}

func (c *RedisCoordinator) ClearCooldown(ctx context.Context, accountID string) error {
	if c == nil || c.redis == nil {
		return errors.New("account coordinator is not configured")
	}
	if accountID == "" {
		return errors.New("account cooldown requires an account ID")
	}
	if _, _, err := c.redis.GetDelete(ctx, c.cooldownKey(accountID)); err != nil {
		return fmt.Errorf("clear account cooldown: %w", err)
	}
	return nil
}

func (c *RedisCoordinator) leaseKey(accountID string) string {
	return fmt.Sprintf("%s:leases:%s", c.namespace, digest(accountID))
}

func (c *RedisCoordinator) affinityKey(model, affinity string) string {
	return c.namespace + ":affinity:" + digest(model+"\x00"+affinity)
}

func (c *RedisCoordinator) cooldownKey(accountID string) string {
	return c.namespace + ":cooldown:" + digest(accountID)
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
