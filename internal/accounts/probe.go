package accounts

import (
	"context"
	"errors"
	"strings"
	"time"
)

var ErrAccountBusy = errors.New("account concurrency limit is currently full")

// AcquireForProbe reserves one concurrency slot for a specifically selected
// account. Administrative probes intentionally bypass scheduling status and
// cooldown checks so a disabled or cooling account can be revalidated.
func (p *Pool) AcquireForProbe(ctx context.Context, accountID string) (*Lease, error) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return nil, ErrNoAccount
	}
	if err := p.ensureLoaded(ctx); err != nil {
		return nil, err
	}

	now := p.now()
	p.mu.Lock()
	state := p.states[accountID]
	if state == nil {
		p.mu.Unlock()
		return nil, ErrNoAccount
	}
	limit := state.account.ConcurrencyLimit
	if limit <= 0 {
		limit = p.policy.DefaultConcurrency
	}
	if state.inflight >= limit {
		p.mu.Unlock()
		return nil, ErrAccountBusy
	}
	state.inflight++
	state.account.LastUsedAt = ptr(now)
	account := state.account
	p.mu.Unlock()

	var coordinatedLease CoordinationLease
	if p.coordinator != nil {
		lease, acquired, err := p.coordinator.AcquireLease(ctx, account.ID, limit, p.policy.LeaseTTL)
		if err != nil {
			p.rollbackReservation(account.ID, time.Time{})
			return nil, err
		}
		if !acquired {
			p.rollbackReservation(account.ID, time.Time{})
			return nil, ErrAccountBusy
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
	lease := &Lease{pool: p, Account: account, Credentials: credentials}
	if p.coordinator != nil {
		lease.coordination = &coordinatedLease
	}
	return lease, nil
}
