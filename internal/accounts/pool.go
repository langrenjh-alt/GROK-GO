package accounts

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/langrenjh-alt/GROK-GO/internal/configevent"
	"github.com/langrenjh-alt/GROK-GO/internal/domain"
)

var ErrNoAccount = errors.New("no eligible upstream account")

type Strategy string

const (
	StrategyAffinity   Strategy = "affinity"
	StrategyPriority   Strategy = "priority"
	StrategyRoundRobin Strategy = "round_robin"
)

func ParseStrategy(value string) (Strategy, error) {
	strategy := Strategy(strings.ToLower(strings.TrimSpace(value)))
	switch strategy {
	case StrategyAffinity, StrategyPriority, StrategyRoundRobin:
		return strategy, nil
	default:
		return "", fmt.Errorf("unsupported account scheduling strategy %q", value)
	}
}

// Store is the minimum persistence contract required by the hot account pool.
// Credential decryption stays behind the store boundary.
type Store interface {
	ListAccounts(context.Context) ([]domain.Account, error)
	Credentials(context.Context, string) (domain.Credentials, error)
	UpdateAccount(context.Context, domain.Account) error
}

type Policy struct {
	Strategy           Strategy
	DefaultConcurrency int
	ForbiddenCooldown  time.Duration
	RateLimitCooldown  map[domain.CredentialKind]time.Duration
	TransientBaseDelay time.Duration
	TransientMaxDelay  time.Duration
	AffinityTTL        time.Duration
	LeaseTTL           time.Duration
	MaxAffinities      int
}

func DefaultPolicy() Policy {
	return Policy{
		Strategy:           StrategyAffinity,
		DefaultConcurrency: 2,
		ForbiddenCooldown:  30 * time.Minute,
		RateLimitCooldown: map[domain.CredentialKind]time.Duration{
			domain.CredentialCLIOAuth:   time.Hour,
			domain.CredentialConsoleSSO: 4 * time.Hour,
			domain.CredentialGrokSSO:    15 * time.Minute,
		},
		TransientBaseDelay: 5 * time.Second,
		TransientMaxDelay:  5 * time.Minute,
		AffinityTTL:        24 * time.Hour,
		LeaseTTL:           30 * time.Minute,
		MaxAffinities:      10_000,
	}
}

type Selection struct {
	Model       domain.ModelSpec
	AffinityKey string
	ExcludeIDs  map[string]struct{}
}

type Feedback struct {
	StatusCode int
	RetryAfter time.Duration
	Quota      *domain.QuotaSnapshot
	Err        error
}

type runtimeState struct {
	account           domain.Account
	inflight          int
	health            float64
	failures          int
	cooldownTill      time.Time
	lastUsedPersisted time.Time
}

type affinityEntry struct {
	accountID string
	expiresAt time.Time
	usedAt    time.Time
}

type Pool struct {
	store       Store
	policy      Policy
	coordinator Coordinator
	now         func() time.Time

	reloadToken chan struct{}
	mu          sync.Mutex
	loaded      bool
	states      map[string]*runtimeState
	affinities  map[string]affinityEntry
	roundRobin  map[string]uint64
	notifier    configevent.Notifier
}

// SetChangeNotifier publishes persistent account state changes to peer
// instances. Reload deliberately does not publish, so remote reconciliation
// cannot create an event loop.
func (p *Pool) SetChangeNotifier(notifier configevent.Notifier) {
	p.mu.Lock()
	p.notifier = notifier
	p.mu.Unlock()
}

func NewPool(store Store, policy Policy) *Pool {
	return NewPoolWithCoordinator(store, policy, nil)
}

func NewPoolWithCoordinator(store Store, policy Policy, coordinator Coordinator) *Pool {
	defaults := DefaultPolicy()
	if policy.DefaultConcurrency <= 0 {
		policy.DefaultConcurrency = defaults.DefaultConcurrency
	}
	if _, err := ParseStrategy(string(policy.Strategy)); err != nil {
		policy.Strategy = defaults.Strategy
	}
	if policy.ForbiddenCooldown <= 0 {
		policy.ForbiddenCooldown = defaults.ForbiddenCooldown
	}
	if policy.RateLimitCooldown == nil {
		policy.RateLimitCooldown = defaults.RateLimitCooldown
	}
	if policy.TransientBaseDelay <= 0 {
		policy.TransientBaseDelay = defaults.TransientBaseDelay
	}
	if policy.TransientMaxDelay < policy.TransientBaseDelay {
		policy.TransientMaxDelay = defaults.TransientMaxDelay
	}
	if policy.AffinityTTL <= 0 {
		policy.AffinityTTL = defaults.AffinityTTL
	}
	if policy.LeaseTTL <= 0 {
		policy.LeaseTTL = defaults.LeaseTTL
	}
	if policy.MaxAffinities <= 0 {
		policy.MaxAffinities = defaults.MaxAffinities
	}
	pool := &Pool{
		store:       store,
		policy:      policy,
		coordinator: coordinator,
		now:         time.Now,
		reloadToken: make(chan struct{}, 1),
		states:      make(map[string]*runtimeState),
		affinities:  make(map[string]affinityEntry),
		roundRobin:  make(map[string]uint64),
	}
	pool.reloadToken <- struct{}{}
	return pool
}

func (p *Pool) Strategy() Strategy {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.policy.Strategy
}

func (p *Pool) SetStrategy(strategy Strategy) error {
	parsed, err := ParseStrategy(string(strategy))
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.policy.Strategy == parsed {
		return nil
	}
	p.policy.Strategy = parsed
	p.affinities = make(map[string]affinityEntry)
	p.roundRobin = make(map[string]uint64)
	return nil
}

// Reload reconciles persistent accounts while retaining per-process inflight
// counters and health observations.
func (p *Pool) Reload(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.reloadToken:
	}
	defer func() { p.reloadToken <- struct{}{} }()

	items, err := p.store.ListAccounts(ctx)
	if err != nil {
		return err
	}
	p.mu.Lock()
	seen := make(map[string]struct{}, len(items))
	for i := range items {
		item := items[i]
		if item.HealthScore <= 0 {
			item.HealthScore = 1
		}
		seen[item.ID] = struct{}{}
		state := p.states[item.ID]
		if state == nil {
			state = &runtimeState{}
			p.states[item.ID] = state
		}
		state.account = item
		state.health = item.HealthScore
		state.failures = item.FailureCount
		if item.LastUsedAt != nil && item.LastUsedAt.After(state.lastUsedPersisted) {
			state.lastUsedPersisted = *item.LastUsedAt
		}
		if item.CooldownUntil != nil && item.CooldownUntil.After(state.cooldownTill) {
			state.cooldownTill = *item.CooldownUntil
		} else if item.CooldownUntil == nil {
			state.cooldownTill = time.Time{}
		}
	}
	for id := range p.states {
		if _, ok := seen[id]; !ok {
			delete(p.states, id)
		}
	}
	for key, entry := range p.affinities {
		state := p.states[entry.accountID]
		if state == nil || state.account.Status != domain.AccountActive {
			delete(p.affinities, key)
		}
	}
	p.loaded = true
	p.mu.Unlock()
	return nil
}

// RemoveAccounts immediately evicts accounts whose persistent deletion has
// already committed. A later full Reload still reconciles the remaining pool.
func (p *Pool) RemoveAccounts(accountIDs []string) {
	removed := make(map[string]struct{}, len(accountIDs))
	for _, accountID := range accountIDs {
		if accountID = strings.TrimSpace(accountID); accountID != "" {
			removed[accountID] = struct{}{}
		}
	}
	if len(removed) == 0 {
		return
	}
	p.mu.Lock()
	for accountID := range removed {
		delete(p.states, accountID)
	}
	for key, affinity := range p.affinities {
		if _, ok := removed[affinity.accountID]; ok {
			delete(p.affinities, key)
		}
	}
	p.mu.Unlock()
}

// ClearCooldown removes coordinated cooldown state after a caller has
// explicitly persisted an active account transition. Periodic reloads must
// not clear these keys because their database snapshot can be older than a
// concurrent failure observed by another process.
func (p *Pool) ClearCooldown(ctx context.Context, accountID string) error {
	if p.coordinator == nil {
		return nil
	}
	return p.coordinator.ClearCooldown(ctx, accountID)
}

func (p *Pool) Acquire(ctx context.Context, selection Selection) (*Lease, error) {
	if err := p.ensureLoaded(ctx); err != nil {
		return nil, err
	}

	p.mu.Lock()
	useAffinity := p.policy.Strategy == StrategyAffinity && selection.AffinityKey != ""
	p.mu.Unlock()
	coordinatedAffinity := ""
	coordinatedAffinityChecked := p.coordinator != nil && useAffinity
	if coordinatedAffinityChecked {
		accountID, found, err := p.coordinator.GetAffinity(ctx, selection.Model.ID, selection.AffinityKey)
		if err != nil {
			return nil, fmt.Errorf("read coordinated account affinity: %w", err)
		}
		if found {
			coordinatedAffinity = accountID
		}
	}

	excluded := make(map[string]struct{}, len(selection.ExcludeIDs))
	for id := range selection.ExcludeIDs {
		excluded[id] = struct{}{}
	}
	capacityExcluded := make(map[string]struct{})
	for {
		now := p.now()
		p.mu.Lock()
		p.pruneAffinitiesLocked(now)
		if coordinatedAffinityChecked {
			if coordinatedAffinity != "" {
				p.setAffinityLocked(selection.Model.ID, selection.AffinityKey, coordinatedAffinity, now)
			} else {
				delete(p.affinities, affinityKey(selection.Model.ID, selection.AffinityKey))
			}
		}
		preferredAffinity := ""
		preferredAtCapacity := false
		staleAffinity := ""
		leaseAffinityEstablished := false
		if useAffinity {
			key := affinityKey(selection.Model.ID, selection.AffinityKey)
			if entry, ok := p.affinities[key]; ok && entry.expiresAt.After(now) {
				preferredAffinity = entry.accountID
				preferredState := p.states[entry.accountID]
				if preferredState == nil || !p.durablyEligibleLocked(preferredState, selection.Model, now) {
					staleAffinity = entry.accountID
					preferredAffinity = ""
					delete(p.affinities, key)
				} else {
					limit := preferredState.account.ConcurrencyLimit
					if limit <= 0 {
						limit = p.policy.DefaultConcurrency
					}
					_, coordinatedCapacityExhausted := capacityExcluded[entry.accountID]
					preferredAtCapacity = preferredState.inflight >= limit || coordinatedCapacityExhausted
				}
			}
		}
		if staleAffinity != "" {
			p.mu.Unlock()
			if p.coordinator != nil && coordinatedAffinity == staleAffinity {
				if err := p.coordinator.ClearAffinity(ctx, selection.Model.ID, selection.AffinityKey, staleAffinity); err != nil {
					return nil, fmt.Errorf("clear unavailable account affinity: %w", err)
				}
				coordinatedAffinity = ""
			}
			continue
		}
		attempt := selection
		attempt.ExcludeIDs = excluded
		state := p.selectLocked(attempt, now)
		if state == nil {
			p.mu.Unlock()
			return nil, ErrNoAccount
		}
		state.inflight++
		state.account.LastUsedAt = ptr(now)
		account := state.account
		affinityReused := preferredAffinity != "" && preferredAffinity == account.ID
		preserveAffinity := preferredAffinity != "" && account.ID != preferredAffinity && preferredAtCapacity
		p.mu.Unlock()

		var coordinatedLease CoordinationLease
		if p.coordinator != nil {
			until, coolingDown, err := p.coordinator.CooldownUntil(ctx, account.ID)
			if err != nil {
				p.rollbackReservation(account.ID, time.Time{})
				return nil, fmt.Errorf("read coordinated account cooldown: %w", err)
			}
			if coolingDown {
				p.rollbackReservation(account.ID, until)
				excluded[account.ID] = struct{}{}
				if coordinatedAffinity != "" && account.ID == coordinatedAffinity {
					p.clearLocalAffinity(selection.Model.ID, selection.AffinityKey, coordinatedAffinity)
					if err := p.coordinator.ClearAffinity(ctx, selection.Model.ID, selection.AffinityKey, coordinatedAffinity); err != nil {
						return nil, fmt.Errorf("clear cooling account affinity: %w", err)
					}
					coordinatedAffinity = ""
				}
				continue
			}

			limit := account.ConcurrencyLimit
			if limit <= 0 {
				limit = p.policy.DefaultConcurrency
			}
			lease, acquired, err := p.coordinator.AcquireLease(ctx, account.ID, limit, p.policy.LeaseTTL)
			if err != nil {
				p.rollbackReservation(account.ID, time.Time{})
				return nil, fmt.Errorf("acquire coordinated account lease: %w", err)
			}
			if !acquired {
				p.rollbackReservation(account.ID, time.Time{})
				excluded[account.ID] = struct{}{}
				capacityExcluded[account.ID] = struct{}{}
				continue
			}
			coordinatedLease = lease
		}

		credentials, err := p.store.Credentials(ctx, account.ID)
		if err != nil {
			p.rollbackReservation(account.ID, time.Time{})
			if p.coordinator != nil {
				err = errors.Join(err, p.coordinator.ReleaseLease(ctx, coordinatedLease))
			}
			return nil, err
		}
		if coordinatedAffinity != "" && account.ID != coordinatedAffinity && !preserveAffinity {
			if err := p.coordinator.ClearAffinity(ctx, selection.Model.ID, selection.AffinityKey, coordinatedAffinity); err != nil {
				p.rollbackReservation(account.ID, time.Time{})
				if p.coordinator != nil {
					err = errors.Join(err, p.coordinator.ReleaseLease(ctx, coordinatedLease))
				}
				return nil, fmt.Errorf("clear stale account affinity: %w", err)
			}
			coordinatedAffinity = ""
		}
		if useAffinity {
			affinityAccountID := account.ID
			affinityEstablished := false
			if preserveAffinity {
				affinityAccountID = preferredAffinity
			}
			if p.coordinator != nil {
				resolved, established, err := p.coordinator.BindAffinity(ctx, selection.Model.ID, selection.AffinityKey, account.ID, p.policy.AffinityTTL)
				if err != nil {
					p.rollbackReservation(account.ID, time.Time{})
					releaseErr := p.coordinator.ReleaseLease(ctx, coordinatedLease)
					return nil, errors.Join(fmt.Errorf("bind coordinated account affinity: %w", err), releaseErr)
				}
				affinityAccountID = resolved
				affinityEstablished = established && resolved == account.ID
				coordinatedAffinity = resolved
			} else {
				affinityEstablished = affinityAccountID == account.ID && !affinityReused
			}
			p.recordAffinity(selection.Model.ID, selection.AffinityKey, affinityAccountID, now)
			leaseAffinityEstablished = affinityEstablished
		}
		lease := &Lease{
			pool: p, Account: account, Credentials: credentials,
			CacheIdentityApplied:     useAffinity,
			AffinityReused:           affinityReused,
			CacheAffinityEstablished: leaseAffinityEstablished,
		}
		if useAffinity {
			lease.affinityModel = selection.Model.ID
			lease.affinityKey = selection.AffinityKey
		}
		if p.coordinator != nil {
			lease.coordination = &coordinatedLease
		}
		return lease, nil
	}
}

func (p *Pool) clearLocalAffinity(model, key, accountID string) {
	if model == "" || key == "" || accountID == "" {
		return
	}
	p.mu.Lock()
	p.clearLocalAffinityLocked(model, key, accountID)
	p.mu.Unlock()
}

func (p *Pool) clearLocalAffinityLocked(model, key, accountID string) {
	if model == "" || key == "" || accountID == "" {
		return
	}
	mapKey := affinityKey(model, key)
	if entry, ok := p.affinities[mapKey]; ok && entry.accountID == accountID {
		delete(p.affinities, mapKey)
	}
}

func (p *Pool) rollbackReservation(accountID string, cooldownUntil time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	state := p.states[accountID]
	if state == nil {
		return
	}
	if state.inflight > 0 {
		state.inflight--
	}
	if cooldownUntil.After(state.cooldownTill) {
		state.cooldownTill = cooldownUntil
	}
}

func (p *Pool) recordAffinity(model, key, accountID string, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pruneAffinitiesLocked(now)
	p.setAffinityLocked(model, key, accountID, now)
}

func (p *Pool) setAffinityLocked(model, key, accountID string, now time.Time) {
	p.affinities[affinityKey(model, key)] = affinityEntry{
		accountID: accountID,
		expiresAt: now.Add(p.policy.AffinityTTL),
		usedAt:    now,
	}
}

func (p *Pool) ensureLoaded(ctx context.Context) error {
	p.mu.Lock()
	loaded := p.loaded
	p.mu.Unlock()
	if loaded {
		return nil
	}
	return p.Reload(ctx)
}

func (p *Pool) selectLocked(selection Selection, now time.Time) *runtimeState {
	var candidates []*runtimeState
	for id, state := range p.states {
		if _, excluded := selection.ExcludeIDs[id]; excluded || !p.eligibleLocked(state, selection.Model, now) {
			continue
		}
		candidates = append(candidates, state)
	}
	if len(candidates) == 0 {
		return nil
	}
	if selection.Model.PreferBest {
		bestTier := tierRank(candidates[0].account.Tier)
		for _, state := range candidates[1:] {
			bestTier = max(bestTier, tierRank(state.account.Tier))
		}
		preferred := candidates[:0]
		for _, state := range candidates {
			if tierRank(state.account.Tier) == bestTier {
				preferred = append(preferred, state)
			}
		}
		candidates = preferred
	}

	if p.policy.Strategy == StrategyAffinity && selection.AffinityKey != "" {
		entry, ok := p.affinities[affinityKey(selection.Model.ID, selection.AffinityKey)]
		if ok && entry.expiresAt.After(now) {
			for _, state := range candidates {
				if state.account.ID == entry.accountID {
					return state
				}
			}
		}
	}
	if p.policy.Strategy == StrategyRoundRobin {
		slices.SortFunc(candidates, func(a, b *runtimeState) int {
			return strings.Compare(a.account.ID, b.account.ID)
		})
		cursor := p.roundRobin[selection.Model.ID]
		p.roundRobin[selection.Model.ID] = cursor + 1
		return candidates[cursor%uint64(len(candidates))]
	}

	best := candidates[0]
	bestScore := score(best, now)
	for _, state := range candidates[1:] {
		candidateScore := score(state, now)
		if candidateScore > bestScore || (candidateScore == bestScore && state.account.ID < best.account.ID) {
			best, bestScore = state, candidateScore
		}
	}
	return best
}

func (p *Pool) eligibleLocked(state *runtimeState, model domain.ModelSpec, now time.Time) bool {
	if !p.durablyEligibleLocked(state, model, now) {
		return false
	}
	limit := state.account.ConcurrencyLimit
	if limit <= 0 {
		limit = p.policy.DefaultConcurrency
	}
	return state.inflight < limit
}

func (p *Pool) durablyEligibleLocked(state *runtimeState, model domain.ModelSpec, now time.Time) bool {
	account := state.account
	if account.Status == domain.AccountCooldown && !state.cooldownTill.After(now) {
		account.Status = domain.AccountActive
		account.CooldownUntil = nil
		state.account = account
	}
	if account.Status != domain.AccountActive || state.cooldownTill.After(now) {
		return false
	}
	if !slices.Contains(model.CredentialKinds, account.Kind) {
		return false
	}
	if len(account.Models) > 0 && !slices.Contains(account.Models, model.ID) && !slices.Contains(account.Models, model.UpstreamModel) {
		return false
	}
	if model.MinimumTier != "" && tierRank(account.Tier) < tierRank(model.MinimumTier) {
		return false
	}
	if fallback := p.policy.RateLimitCooldown[account.Kind]; quotaUnavailableUntil(account.Quota, now, fallback).After(now) {
		return false
	}
	return true
}

func score(state *runtimeState, now time.Time) float64 {
	quotaScore := 1.0
	quota := state.account.Quota
	if !quota.RequestsUnlimited && quota.RequestsLimit > 0 {
		quotaScore = math.Min(quotaScore, quotaFraction(quota.RequestsRemaining, quota.RequestsLimit))
	}
	if !quota.TokensUnlimited && quota.TokensLimit > 0 {
		quotaScore = math.Min(quotaScore, quotaFraction(quota.TokensRemaining, quota.TokensLimit))
	}
	recentPenalty := 0.0
	if state.account.LastUsedAt != nil {
		age := now.Sub(*state.account.LastUsedAt)
		if age < time.Minute {
			recentPenalty = 15 * (1 - age.Seconds()/60)
		}
	}
	return float64(state.account.Priority)*1000 + state.health*100 + quotaScore*25 - float64(state.inflight)*20 - float64(min(state.failures, 10))*4 - recentPenalty
}

func quotaFraction(remaining, limit int64) float64 {
	return math.Max(0, math.Min(1, float64(remaining)/float64(limit)))
}

func (p *Pool) release(ctx context.Context, lease *Lease, feedback Feedback) error {
	now := p.now()
	var accountToUpdate *domain.Account
	var cooldownUntil time.Time
	publishChange := false
	invalidateAffinity := false
	p.mu.Lock()
	state := p.states[lease.Account.ID]
	if state == nil {
		invalidateAffinity = true
	} else {
		if state.inflight > 0 {
			state.inflight--
		}
		account := state.account
		if !bytes.Equal(account.CredentialCipher, lease.Account.CredentialCipher) ||
			!bytes.Equal(account.CredentialFingerprint, lease.Account.CredentialFingerprint) {
			quotaUntil := quotaUnavailableUntil(account.Quota, now, p.policy.RateLimitCooldown[account.Kind])
			unavailable := account.Status != domain.AccountActive || state.cooldownTill.After(now) || quotaUntil.After(now)
			if unavailable {
				p.clearLocalAffinityLocked(lease.affinityModel, lease.affinityKey, lease.Account.ID)
			}
			coordinatedUntil := state.cooldownTill
			if quotaUntil.After(coordinatedUntil) {
				coordinatedUntil = quotaUntil
			}
			if isTerminalAccountStatus(account.Status) && !coordinatedUntil.After(now) {
				coordinatedUntil = now.Add(p.policy.ForbiddenCooldown)
			}
			p.mu.Unlock()
			var result error
			if p.coordinator != nil && unavailable {
				if coordinatedUntil.After(now) {
					result = errors.Join(result, p.coordinator.SetCooldown(ctx, lease.Account.ID, coordinatedUntil))
				}
				if lease.affinityModel != "" && lease.affinityKey != "" {
					result = errors.Join(result, p.coordinator.ClearAffinity(ctx, lease.affinityModel, lease.affinityKey, lease.Account.ID))
				}
			}
			if p.coordinator != nil && lease.coordination != nil {
				result = errors.Join(result, p.coordinator.ReleaseLease(ctx, *lease.coordination))
			}
			return result
		}
		persist := false
		if state.lastUsedPersisted.IsZero() || now.Sub(state.lastUsedPersisted) >= 30*time.Second {
			state.lastUsedPersisted = now
			persist = true
		}
		if feedback.Quota != nil {
			account.Quota = *feedback.Quota
			persist = true
			publishChange = true
		}
		terminalStateChanged := account.Status != lease.Account.Status && isTerminalAccountStatus(account.Status)
		concurrentCooldown := state.cooldownTill.After(now) && account.Status == domain.AccountCooldown
		quotaReset, quotaExhausted := exhaustedQuotaReset(account.Quota, account.Kind, p.policy, now, feedback.Quota != nil)
		switch {
		case terminalStateChanged:
			invalidateAffinity = true
			cooldownUntil = now.Add(p.policy.ForbiddenCooldown)
		case feedback.StatusCode == 429:
			invalidateAffinity = true
			persist = true
			publishChange = true
			state.health = math.Max(.05, state.health*.45)
			state.failures++
			cooldown := feedback.RetryAfter
			if cooldown <= 0 {
				cooldown = p.policy.RateLimitCooldown[account.Kind]
			}
			if cooldown <= 0 {
				cooldown = 15 * time.Minute
			}
			candidate := now.Add(cooldown)
			if quotaExhausted && quotaReset.After(candidate) {
				candidate = quotaReset
			}
			if state.cooldownTill.After(candidate) {
				candidate = state.cooldownTill
			}
			state.cooldownTill = candidate
			cooldownUntil = candidate
			account.Status = domain.AccountCooldown
			account.CooldownUntil = ptr(candidate)
			account.LastError = feedbackMessage(feedback)
		case isPermanentCredentialFailure(feedback.StatusCode):
			invalidateAffinity = true
			persist = true
			publishChange = true
			state.health = math.Max(.05, state.health*.25)
			state.failures++
			state.cooldownTill = time.Time{}
			account.Status = domain.AccountDisabled
			account.CooldownUntil = nil
			account.LastError = feedbackMessage(feedback)
			cooldownUntil = now.Add(p.policy.ForbiddenCooldown)
		case quotaExhausted:
			invalidateAffinity = true
			persist = true
			publishChange = true
			state.health = math.Max(.05, state.health*.9)
			state.failures++
			if state.cooldownTill.After(quotaReset) {
				quotaReset = state.cooldownTill
			}
			state.cooldownTill = quotaReset
			cooldownUntil = quotaReset
			account.Status = domain.AccountCooldown
			account.CooldownUntil = ptr(quotaReset)
			account.LastError = "upstream quota exhausted"
		case feedback.StatusCode >= 200 && feedback.StatusCode < 400 && feedback.Err == nil && concurrentCooldown:
			// A request acquired before a concurrent failure must not erase its
			// unexpired cooldown when that older request later succeeds.
			invalidateAffinity = true
			cooldownUntil = state.cooldownTill
		case feedback.StatusCode >= 200 && feedback.StatusCode < 400 && feedback.Err == nil:
			recovering := state.health < 1 || state.failures != 0 || account.Status != domain.AccountActive || account.CooldownUntil != nil || account.LastError != ""
			state.health = math.Min(1, state.health+0.12)
			state.failures = 0
			state.cooldownTill = time.Time{}
			account.Status = domain.AccountActive
			account.CooldownUntil = nil
			account.LastError = ""
			if recovering {
				persist = true
				publishChange = true
			}
		case feedback.StatusCode == 499:
			// Client cancellation is not an upstream health failure.
		case feedback.Err != nil || feedback.StatusCode >= 500:
			invalidateAffinity = true
			persist = true
			publishChange = true
			state.health = math.Max(.05, state.health*.75)
			state.failures++
			cooldown := transientBackoff(p.policy, state.failures)
			state.cooldownTill = now.Add(cooldown)
			cooldownUntil = state.cooldownTill
			account.Status = domain.AccountCooldown
			account.CooldownUntil = ptr(state.cooldownTill)
			account.LastError = feedbackMessage(feedback)
		}
		account.HealthScore = state.health
		account.FailureCount = state.failures
		state.account = account
		if persist {
			accountToUpdate = ptr(account)
		}
	}
	if invalidateAffinity {
		p.clearLocalAffinityLocked(lease.affinityModel, lease.affinityKey, lease.Account.ID)
	}
	p.mu.Unlock()

	var result error
	if p.coordinator != nil {
		if !cooldownUntil.IsZero() {
			result = errors.Join(result, p.coordinator.SetCooldown(ctx, lease.Account.ID, cooldownUntil))
		}
		if invalidateAffinity && lease.affinityModel != "" && lease.affinityKey != "" {
			result = errors.Join(result, p.coordinator.ClearAffinity(ctx, lease.affinityModel, lease.affinityKey, lease.Account.ID))
		}
	}
	readyToNotify := false
	if accountToUpdate != nil {
		if err := p.store.UpdateAccount(ctx, *accountToUpdate); err != nil {
			result = errors.Join(result, err)
		} else if publishChange {
			if err := p.Reload(ctx); err != nil {
				result = errors.Join(result, err)
			} else {
				readyToNotify = true
			}
		}
		if publishChange && readyToNotify {
			p.mu.Lock()
			notifier := p.notifier
			p.mu.Unlock()
			if notifier != nil {
				result = errors.Join(result, notifier.Notify(ctx, configevent.ScopeAccounts))
			}
		}
	}
	if p.coordinator != nil && lease.coordination != nil {
		result = errors.Join(result, p.coordinator.ReleaseLease(ctx, *lease.coordination))
	}
	return result
}

func isPermanentCredentialFailure(status int) bool {
	return status == 401 || status == 403 || status == 423
}

func isTerminalAccountStatus(status domain.AccountStatus) bool {
	switch status {
	case domain.AccountDisabled, domain.AccountExpired, domain.AccountError:
		return true
	default:
		return false
	}
}

func exhaustedQuotaReset(quota domain.QuotaSnapshot, kind domain.CredentialKind, policy Policy, now time.Time, freshlyObserved bool) (time.Time, bool) {
	if !quotaExhausted(quota) {
		return time.Time{}, false
	}
	fallback := policy.RateLimitCooldown[kind]
	if fallback <= 0 {
		fallback = 15 * time.Minute
	}
	until := quotaUnavailableUntil(quota, now, fallback)
	if !until.After(now) {
		if !freshlyObserved {
			return time.Time{}, false
		}
		until = now.Add(fallback)
	}
	return until, true
}

func quotaExhausted(quota domain.QuotaSnapshot) bool {
	requests := !quota.RequestsUnlimited && quota.RequestsLimit > 0 && quota.RequestsRemaining <= 0
	tokens := !quota.TokensUnlimited && quota.TokensLimit > 0 && quota.TokensRemaining <= 0
	return requests || tokens
}

func quotaUnavailableUntil(quota domain.QuotaSnapshot, now time.Time, fallback time.Duration) time.Time {
	if !quotaExhausted(quota) {
		return time.Time{}
	}
	if quota.ResetAt != nil {
		if quota.ResetAt.After(now) {
			return *quota.ResetAt
		}
		return time.Time{}
	}
	if fallback > 0 && !quota.ObservedAt.IsZero() {
		until := quota.ObservedAt.Add(fallback)
		if until.After(now) {
			return until
		}
	}
	return time.Time{}
}

// QuotaUnavailableUntil reports the active block window for a quota snapshot.
func QuotaUnavailableUntil(quota domain.QuotaSnapshot, kind domain.CredentialKind, now time.Time) time.Time {
	return quotaUnavailableUntil(quota, now, DefaultPolicy().RateLimitCooldown[kind])
}

func transientBackoff(policy Policy, failures int) time.Duration {
	delay := policy.TransientBaseDelay
	for attempt := 1; attempt < failures && delay < policy.TransientMaxDelay; attempt++ {
		delay *= 2
		if delay >= policy.TransientMaxDelay {
			return policy.TransientMaxDelay
		}
	}
	return delay
}

func (p *Pool) pruneAffinitiesLocked(now time.Time) {
	for key, entry := range p.affinities {
		if !entry.expiresAt.After(now) {
			delete(p.affinities, key)
		}
	}
	for len(p.affinities) >= p.policy.MaxAffinities {
		var oldestKey string
		var oldest time.Time
		for key, entry := range p.affinities {
			if oldestKey == "" || entry.usedAt.Before(oldest) {
				oldestKey, oldest = key, entry.usedAt
			}
		}
		delete(p.affinities, oldestKey)
	}
}

func affinityKey(model, key string) string { return model + "\x00" + key }

func tierRank(tier string) int {
	switch strings.ToLower(tier) {
	case "heavy", "enterprise":
		return 2
	case "super", "pro", "premium":
		return 1
	default:
		return 0
	}
}

func feedbackMessage(feedback Feedback) string {
	if feedback.StatusCode > 0 {
		return "upstream status " + itoa(feedback.StatusCode)
	}
	if errors.Is(feedback.Err, context.DeadlineExceeded) {
		return "upstream request timed out"
	}
	if errors.Is(feedback.Err, context.Canceled) {
		return "upstream request was canceled"
	}
	if feedback.Err != nil {
		return "upstream transport failed"
	}
	return ""
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

func ptr[T any](value T) *T { return &value }

type Lease struct {
	Account                  domain.Account
	Credentials              domain.Credentials
	CacheIdentityApplied     bool
	AffinityReused           bool
	CacheAffinityEstablished bool
	pool                     *Pool
	coordination             *CoordinationLease
	affinityModel            string
	affinityKey              string
	once                     sync.Once
	releaseErr               error
}

func (l *Lease) Release(ctx context.Context, feedback Feedback) error {
	if l == nil || l.pool == nil {
		return nil
	}
	l.once.Do(func() { l.releaseErr = l.pool.release(ctx, l, feedback) })
	return l.releaseErr
}
