package database

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	redislib "github.com/redis/go-redis/v9"

	"github.com/langrenjh-alt/GROK-GO/internal/config"
	"github.com/langrenjh-alt/GROK-GO/internal/configevent"
)

var (
	ErrRedisDisabled = errors.New("Redis is disabled")
	ErrCacheMiss     = errors.New("cache miss")
)

type Redis struct {
	client *redislib.Client
	prefix string
}

func OpenRedis(ctx context.Context, cfg config.RedisConfig) (*Redis, error) {
	if !cfg.Enabled() {
		return nil, ErrRedisDisabled
	}
	options, err := redislib.ParseURL(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("parse Redis URL: %w", err)
	}
	options.DialTimeout = cfg.DialTimeout
	options.ReadTimeout = cfg.ReadTimeout
	options.WriteTimeout = cfg.WriteTimeout
	client := redislib.NewClient(options)
	redis := &Redis{client: client, prefix: cfg.KeyPrefix}
	healthCtx, cancel := context.WithTimeout(ctx, cfg.HealthTimeout)
	defer cancel()
	if err := redis.Health(healthCtx); err != nil {
		_ = client.Close()
		return nil, err
	}
	return redis, nil
}

func (r *Redis) Key(parts ...string) string {
	return r.prefix + strings.Join(parts, ":")
}

func (r *Redis) Health(ctx context.Context) error {
	if r == nil || r.client == nil {
		return ErrRedisDisabled
	}
	if err := r.client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("ping Redis: %w", err)
	}
	return nil
}

func (r *Redis) Get(ctx context.Context, key string) ([]byte, error) {
	value, err := r.client.Get(ctx, r.Key(key)).Bytes()
	if errors.Is(err, redislib.Nil) {
		return nil, ErrCacheMiss
	}
	if err != nil {
		return nil, fmt.Errorf("Redis GET: %w", err)
	}
	return value, nil
}

func (r *Redis) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if err := r.client.Set(ctx, r.Key(key), value, ttl).Err(); err != nil {
		return fmt.Errorf("Redis SET: %w", err)
	}
	return nil
}

func (r *Redis) SetNX(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	if r == nil || r.client == nil {
		return false, ErrRedisDisabled
	}
	set, err := r.client.SetNX(ctx, r.Key(key), value, ttl).Result()
	if err != nil {
		return false, fmt.Errorf("Redis SETNX: %w", err)
	}
	return set, nil
}

// GetDelete atomically consumes a value. The boolean reports whether the key existed.
func (r *Redis) GetDelete(ctx context.Context, key string) ([]byte, bool, error) {
	if r == nil || r.client == nil {
		return nil, false, ErrRedisDisabled
	}
	const script = `
local value = redis.call('GET', KEYS[1])
if not value then return nil end
redis.call('DEL', KEYS[1])
return value`
	result, err := r.client.Eval(ctx, script, []string{r.Key(key)}).Result()
	if errors.Is(err, redislib.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("Redis GETDEL: %w", err)
	}
	switch value := result.(type) {
	case string:
		return []byte(value), true, nil
	case []byte:
		return append([]byte(nil), value...), true, nil
	case nil:
		return nil, false, nil
	default:
		return nil, false, fmt.Errorf("Redis GETDEL returned %T", result)
	}
}

// CompareDelete releases a lease only when it is still owned by expected.
func (r *Redis) CompareDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	if r == nil || r.client == nil {
		return false, ErrRedisDisabled
	}
	const script = `
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0`
	deleted, err := r.client.Eval(ctx, script, []string{r.Key(key)}, expected).Int64()
	if err != nil {
		return false, fmt.Errorf("Redis compare-delete: %w", err)
	}
	return deleted == 1, nil
}

// CompareExpire refreshes a value's TTL only while ownership is unchanged.
func (r *Redis) CompareExpire(ctx context.Context, key string, expected []byte, ttl time.Duration) (bool, error) {
	if r == nil || r.client == nil {
		return false, ErrRedisDisabled
	}
	const script = `
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('PEXPIRE', KEYS[1], ARGV[2])
end
return 0`
	refreshed, err := r.client.Eval(ctx, script, []string{r.Key(key)}, expected, ttl.Milliseconds()).Int64()
	if err != nil {
		return false, fmt.Errorf("Redis compare-expire: %w", err)
	}
	return refreshed == 1, nil
}

// SetIfGreater stores a numeric deadline only when it advances the current
// value, and expires the key at that same deadline.
func (r *Redis) SetIfGreater(ctx context.Context, key string, value int64, expiresAt time.Time) (bool, error) {
	if r == nil || r.client == nil {
		return false, ErrRedisDisabled
	}
	const script = `
local candidate = tonumber(ARGV[1])
local current = tonumber(redis.call('GET', KEYS[1]))
if current and current >= candidate then return 0 end
redis.call('SET', KEYS[1], ARGV[1])
redis.call('PEXPIREAT', KEYS[1], ARGV[2])
return 1`
	updated, err := r.client.Eval(ctx, script, []string{r.Key(key)}, value, expiresAt.UnixMilli()).Int64()
	if err != nil {
		return false, fmt.Errorf("Redis set-if-greater: %w", err)
	}
	return updated == 1, nil
}

// AcquireLeaseSlot atomically removes expired owners and reserves one slot in
// an owner-keyed sorted set. Its cost is constant with respect to the limit.
func (r *Redis) AcquireLeaseSlot(ctx context.Context, key, owner string, limit int, ttl time.Duration) (bool, error) {
	if r == nil || r.client == nil {
		return false, ErrRedisDisabled
	}
	if limit <= 0 || ttl <= 0 || strings.TrimSpace(owner) == "" {
		return false, errors.New("Redis lease arguments are invalid")
	}
	const script = `
local tm = redis.call('TIME')
local now = tonumber(tm[1]) * 1000 + math.floor(tonumber(tm[2]) / 1000)
local ttl = tonumber(ARGV[3])
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now)
if redis.call('ZSCORE', KEYS[1], ARGV[1]) then
  redis.call('ZADD', KEYS[1], now + ttl, ARGV[1])
  local latest = redis.call('ZRANGE', KEYS[1], -1, -1, 'WITHSCORES')
  redis.call('PEXPIREAT', KEYS[1], math.ceil(tonumber(latest[2])))
  return 1
end
if redis.call('ZCARD', KEYS[1]) >= tonumber(ARGV[2]) then return 0 end
redis.call('ZADD', KEYS[1], now + ttl, ARGV[1])
local latest = redis.call('ZRANGE', KEYS[1], -1, -1, 'WITHSCORES')
redis.call('PEXPIREAT', KEYS[1], math.ceil(tonumber(latest[2])))
return 1`
	acquired, err := r.client.Eval(ctx, script, []string{r.Key(key)}, owner, limit, ttl.Milliseconds()).Int64()
	if err != nil {
		return false, fmt.Errorf("Redis acquire owner lease: %w", err)
	}
	return acquired == 1, nil
}

func (r *Redis) ReleaseLeaseSlot(ctx context.Context, key, owner string) error {
	if r == nil || r.client == nil {
		return ErrRedisDisabled
	}
	const script = `
local removed = redis.call('ZREM', KEYS[1], ARGV[1])
local latest = redis.call('ZRANGE', KEYS[1], -1, -1, 'WITHSCORES')
if #latest == 0 then
  redis.call('DEL', KEYS[1])
else
  redis.call('PEXPIREAT', KEYS[1], math.ceil(tonumber(latest[2])))
end
return removed`
	if err := r.client.Eval(ctx, script, []string{r.Key(key)}, owner).Err(); err != nil {
		return fmt.Errorf("Redis release owner lease: %w", err)
	}
	return nil
}

func (r *Redis) Delete(ctx context.Context, keys ...string) error {
	prefixed := make([]string, len(keys))
	for i, key := range keys {
		prefixed[i] = r.Key(key)
	}
	if len(prefixed) == 0 {
		return nil
	}
	if err := r.client.Del(ctx, prefixed...).Err(); err != nil {
		return fmt.Errorf("Redis DEL: %w", err)
	}
	return nil
}

func (r *Redis) IncrementWindow(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	return r.AddWindow(ctx, key, 1, ttl)
}

// AddWindow atomically reads or increments a TTL-bound counter. A zero delta
// performs a read without creating the key, which is used for quota preflight
// checks before the final token usage is known.
func (r *Redis) AddWindow(ctx context.Context, key string, delta int64, ttl time.Duration) (int64, error) {
	if r == nil || r.client == nil {
		return 0, ErrRedisDisabled
	}
	if delta < 0 {
		return 0, errors.New("Redis window delta must not be negative")
	}
	const script = `
local delta = tonumber(ARGV[1])
local value = tonumber(redis.call('GET', KEYS[1]) or '0')
if delta > 0 then
  value = redis.call('INCRBY', KEYS[1], delta)
  if redis.call('PTTL', KEYS[1]) < 0 then
    redis.call('PEXPIRE', KEYS[1], ARGV[2])
  end
end
return value`
	value, err := r.client.Eval(ctx, script, []string{r.Key(key)}, delta, ttl.Milliseconds()).Int64()
	if err != nil {
		return 0, fmt.Errorf("Redis add window: %w", err)
	}
	return value, nil
}

func (r *Redis) AcquireSlot(ctx context.Context, key string, limit int, ttl time.Duration) (bool, error) {
	if r == nil || r.client == nil {
		return false, ErrRedisDisabled
	}
	if limit <= 0 {
		return true, nil
	}
	const script = `
local current = tonumber(redis.call('GET', KEYS[1]) or '0')
local limit = tonumber(ARGV[1])
if current >= limit then return 0 end
current = redis.call('INCR', KEYS[1])
redis.call('PEXPIRE', KEYS[1], ARGV[2])
return 1`
	value, err := r.client.Eval(ctx, script, []string{r.Key(key)}, limit, ttl.Milliseconds()).Int64()
	if err != nil {
		return false, fmt.Errorf("Redis acquire slot: %w", err)
	}
	return value == 1, nil
}

func (r *Redis) ReleaseSlot(ctx context.Context, key string) error {
	if r == nil || r.client == nil {
		return ErrRedisDisabled
	}
	const script = `
local current = tonumber(redis.call('GET', KEYS[1]) or '0')
if current <= 1 then
  redis.call('DEL', KEYS[1])
  return 0
end
return redis.call('DECR', KEYS[1])`
	if err := r.client.Eval(ctx, script, []string{r.Key(key)}).Err(); err != nil {
		return fmt.Errorf("Redis release slot: %w", err)
	}
	return nil
}

func (r *Redis) Publish(ctx context.Context, channel string, payload []byte) error {
	if r == nil || r.client == nil {
		return ErrRedisDisabled
	}
	channel = strings.TrimSpace(channel)
	if channel == "" {
		return errors.New("Redis publish channel is required")
	}
	if err := r.client.Publish(ctx, r.Key(channel), payload).Err(); err != nil {
		return fmt.Errorf("Redis PUBLISH: %w", err)
	}
	return nil
}

func (r *Redis) Subscribe(ctx context.Context, channel string) (configevent.Subscription, error) {
	if r == nil || r.client == nil {
		return nil, ErrRedisDisabled
	}
	channel = strings.TrimSpace(channel)
	if channel == "" {
		return nil, errors.New("Redis subscribe channel is required")
	}
	pubsub := r.client.Subscribe(ctx, r.Key(channel))
	subscription := &redisSubscription{pubsub: pubsub}
	stopCancellationWatch := subscription.cancelOnDone(ctx)
	if _, err := pubsub.Receive(ctx); err != nil {
		stopCancellationWatch()
		_ = subscription.Close()
		return nil, fmt.Errorf("Redis SUBSCRIBE: %w", err)
	}
	stopCancellationWatch()
	return subscription, nil
}

type redisSubscription struct {
	pubsub   *redislib.PubSub
	close    sync.Once
	closeErr error
}

func (s *redisSubscription) Receive(ctx context.Context) ([]byte, error) {
	if s == nil || s.pubsub == nil {
		return nil, ErrRedisDisabled
	}
	stopCancellationWatch := s.cancelOnDone(ctx)
	message, err := s.pubsub.ReceiveMessage(ctx)
	stopCancellationWatch()
	if err != nil {
		return nil, fmt.Errorf("Redis receive message: %w", err)
	}
	return []byte(message.Payload), nil
}

func (s *redisSubscription) Close() error {
	if s == nil || s.pubsub == nil {
		return nil
	}
	s.close.Do(func() { s.closeErr = s.pubsub.Close() })
	return s.closeErr
}

func (s *redisSubscription) cancelOnDone(ctx context.Context) func() {
	finished := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = s.Close()
		case <-finished:
		}
	}()
	return func() { close(finished) }
}

func (r *Redis) Close() error {
	if r == nil || r.client == nil {
		return nil
	}
	return r.client.Close()
}
