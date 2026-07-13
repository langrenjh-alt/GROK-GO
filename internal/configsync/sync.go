package configsync

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/langrenjh-alt/GROK-GO/internal/accounts"
	"github.com/langrenjh-alt/GROK-GO/internal/configevent"
	"github.com/langrenjh-alt/GROK-GO/internal/runtimecfg"
)

const defaultChannel = "configuration:changes"

type SettingsStore interface {
	LoadSettings(context.Context) (map[string]any, error)
}

type AccountTarget interface {
	SetStrategy(accounts.Strategy) error
	Reload(context.Context) error
}

type Config struct {
	Bus             configevent.Bus
	Settings        SettingsStore
	RuntimeSettings *runtimecfg.Runtime
	Accounts        AccountTarget
	InstanceID      string
	Channel         string
	ReconnectMin    time.Duration
	ReconnectMax    time.Duration
	OnError         func(error)
}

type Synchronizer struct {
	bus             configevent.Bus
	settings        SettingsStore
	runtimeSettings *runtimecfg.Runtime
	accounts        AccountTarget
	instanceID      string
	channel         string
	reconnectMin    time.Duration
	reconnectMax    time.Duration
	onError         func(error)
}

type notification struct {
	Source string            `json:"source"`
	Scope  configevent.Scope `json:"scope"`
}

func New(config Config) (*Synchronizer, error) {
	if config.Bus == nil || config.Settings == nil || config.RuntimeSettings == nil || config.Accounts == nil {
		return nil, errors.New("configuration synchronization dependencies are required")
	}
	instanceID := strings.TrimSpace(config.InstanceID)
	if instanceID == "" {
		var material [12]byte
		if _, err := rand.Read(material[:]); err != nil {
			return nil, fmt.Errorf("generate configuration instance ID: %w", err)
		}
		instanceID = hex.EncodeToString(material[:])
	}
	channel := strings.TrimSpace(config.Channel)
	if channel == "" {
		channel = defaultChannel
	}
	reconnectMin := config.ReconnectMin
	if reconnectMin <= 0 {
		reconnectMin = 100 * time.Millisecond
	}
	reconnectMax := config.ReconnectMax
	if reconnectMax <= 0 {
		reconnectMax = 5 * time.Second
	}
	if reconnectMax < reconnectMin {
		reconnectMax = reconnectMin
	}
	return &Synchronizer{
		bus: config.Bus, settings: config.Settings, runtimeSettings: config.RuntimeSettings,
		accounts: config.Accounts, instanceID: instanceID, channel: channel,
		reconnectMin: reconnectMin, reconnectMax: reconnectMax, onError: config.OnError,
	}, nil
}

func (s *Synchronizer) Notify(ctx context.Context, scope configevent.Scope) error {
	if !validScope(scope) {
		return fmt.Errorf("unsupported configuration notification scope %q", scope)
	}
	payload, err := json.Marshal(notification{Source: s.instanceID, Scope: scope})
	if err != nil {
		return fmt.Errorf("encode configuration notification: %w", err)
	}
	if err := s.bus.Publish(ctx, s.channel, payload); err != nil {
		return fmt.Errorf("publish configuration notification: %w", err)
	}
	return nil
}

// Run subscribes until ctx is canceled. Every successful subscription first
// reloads PostgreSQL so changes published during a Redis outage are recovered.
func (s *Synchronizer) Run(ctx context.Context) error {
	delay := s.reconnectMin
	for ctx.Err() == nil {
		subscription, err := s.bus.Subscribe(ctx, s.channel)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			s.report(fmt.Errorf("subscribe to configuration notifications: %w", err))
			if !wait(ctx, delay) {
				return nil
			}
			delay = nextDelay(delay, s.reconnectMax)
			continue
		}
		delay = s.reconnectMin
		if err := s.reload(ctx, ""); err != nil && ctx.Err() == nil {
			s.report(fmt.Errorf("reload configuration after subscribe: %w", err))
		}
		receiveErr := s.consume(ctx, subscription)
		closeErr := subscription.Close()
		if ctx.Err() != nil {
			return nil
		}
		if receiveErr != nil {
			s.report(receiveErr)
		}
		if closeErr != nil {
			s.report(fmt.Errorf("close configuration subscription: %w", closeErr))
		}
		if !wait(ctx, delay) {
			return nil
		}
		delay = nextDelay(delay, s.reconnectMax)
	}
	return nil
}

func (s *Synchronizer) consume(ctx context.Context, subscription configevent.Subscription) error {
	for {
		payload, err := subscription.Receive(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("receive configuration notification: %w", err)
		}
		var event notification
		if err := json.Unmarshal(payload, &event); err != nil {
			s.report(fmt.Errorf("decode configuration notification: %w", err))
			continue
		}
		if strings.TrimSpace(event.Source) == "" || !validScope(event.Scope) {
			s.report(errors.New("configuration notification is invalid"))
			continue
		}
		if event.Source == s.instanceID {
			continue
		}
		if err := s.reload(ctx, event.Scope); err != nil && ctx.Err() == nil {
			s.report(fmt.Errorf("reload %s configuration: %w", event.Scope, err))
		}
	}
}

func (s *Synchronizer) reload(ctx context.Context, scope configevent.Scope) error {
	if scope == configevent.ScopeAccounts {
		return s.accounts.Reload(ctx)
	}
	persisted, err := s.settings.LoadSettings(ctx)
	if err != nil {
		if scope == "" {
			return errors.Join(err, s.accounts.Reload(ctx))
		}
		return err
	}
	var result error
	if scope == "" || scope == configevent.ScopeRuntimeSettings {
		configured, resolveErr := runtimecfg.Resolve(s.runtimeSettings.Defaults(), persisted)
		if resolveErr != nil {
			result = errors.Join(result, resolveErr)
		} else {
			s.runtimeSettings.Apply(configured)
		}
	}
	if scope == "" || scope == configevent.ScopeAccountStrategy {
		strategy := accounts.StrategyAffinity
		applyStrategy := true
		if value, exists := persisted["account_scheduling_strategy"]; exists && value != nil && strings.TrimSpace(fmt.Sprint(value)) != "" {
			parsed, parseErr := accounts.ParseStrategy(fmt.Sprint(value))
			if parseErr != nil {
				result = errors.Join(result, parseErr)
				applyStrategy = false
			} else {
				strategy = parsed
			}
		}
		if applyStrategy {
			if err := s.accounts.SetStrategy(strategy); err != nil {
				result = errors.Join(result, err)
			}
		}
	}
	if scope == "" {
		result = errors.Join(result, s.accounts.Reload(ctx))
	}
	return result
}

func (s *Synchronizer) report(err error) {
	if err != nil && s.onError != nil {
		s.onError(err)
	}
}

func validScope(scope configevent.Scope) bool {
	return scope == configevent.ScopeRuntimeSettings || scope == configevent.ScopeAccountStrategy || scope == configevent.ScopeAccounts
}

func wait(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func nextDelay(current, maximum time.Duration) time.Duration {
	if current >= maximum/2 {
		return maximum
	}
	return current * 2
}
