package subscriptions

import (
	"context"
	"time"

	"weave-os/router/internal/auth"
)

func mergePoolAccount(existing, incoming Account, now time.Time) Account {
	if incoming.QuotaResetGeneration > existing.QuotaResetGeneration {
		if incoming.TokenRefreshVersion < existing.TokenRefreshVersion {
			incoming.TokenRefreshVersion = existing.TokenRefreshVersion
			incoming.AccessToken, incoming.AccessTokenExpiresAt = existing.AccessToken, existing.AccessTokenExpiresAt
		}
		return incoming
	}
	if incoming.QuotaResetGeneration < existing.QuotaResetGeneration {
		incoming.QuotaResetGeneration = existing.QuotaResetGeneration
		incoming.State, incoming.CooldownTil = existing.State, existing.CooldownTil
		incoming.AccessToken, incoming.AccessTokenExpiresAt = existing.AccessToken, existing.AccessTokenExpiresAt
		incoming.TokenRefreshVersion = existing.TokenRefreshVersion
		return incoming
	}
	if incoming.TokenRefreshVersion < existing.TokenRefreshVersion ||
		incoming.AccessToken == "" && incoming.TokenRefreshVersion == existing.TokenRefreshVersion {
		incoming.TokenRefreshVersion = existing.TokenRefreshVersion
		incoming.AccessToken, incoming.AccessTokenExpiresAt = existing.AccessToken, existing.AccessTokenExpiresAt
	}
	if existing.State == auth.SubscriptionAccountStateActive && incoming.State == auth.SubscriptionAccountStateUnknown {
		incoming.State = existing.State
	}
	if existing.CooldownTil.After(incoming.CooldownTil) ||
		existing.State == auth.SubscriptionAccountStateCooldown && incoming.State == auth.SubscriptionAccountStateExhausted && existing.CooldownTil.After(now) {
		if existing.CooldownTil.After(incoming.CooldownTil) {
			incoming.CooldownTil = existing.CooldownTil
		}
		incoming.State = existing.State
	}
	return incoming
}

func (r *Runtime) ExhaustLease(ctx context.Context, owner auth.SubscriptionOwner, provider Provider, lease Lease, resetAt time.Time) error {
	if provider != ProviderCodex || r.resetStore == nil {
		return r.Exhaust(ctx, owner, provider, lease.AccountID, resetAt)
	}
	physicalOwner, err := r.admit(ctx, lease.AccountID, owner)
	if err != nil {
		return err
	}
	applied, err := r.resetStore.RecordCodexExhaustion(ctx, physicalOwner, lease, resetAt)
	if err != nil || !applied {
		return err
	}
	r.manager.pool("account:"+lease.AccountID, provider).exhaustLease(lease, resetAt)
	return nil
}

func (p *Pool) exhaustLease(lease Lease, resetAt time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	state := p.accounts[lease.AccountID]
	if state == nil || !state.account.Enabled || state.account.QuotaResetGeneration != lease.QuotaResetGeneration {
		return
	}
	if state.account.CooldownTil.Before(resetAt) {
		state.account.CooldownTil = resetAt
	}
	state.account.State = auth.SubscriptionAccountStateExhausted
}

func (p *Pool) applyRefreshGeneration(accountID string, refreshed Account, err error) (Account, error) {
	state := p.accounts[accountID]
	if err != nil || state == nil {
		return refreshed, err
	}
	if refreshed.QuotaResetGeneration < state.account.QuotaResetGeneration || refreshed.TokenRefreshVersion < state.account.TokenRefreshVersion {
		current := state.account
		if refreshed.QuotaResetGeneration < current.QuotaResetGeneration && refreshed.TokenRefreshVersion > current.TokenRefreshVersion {
			return Account{}, ErrNoAvailableAccount
		}
		merged := mergePoolAccount(current, refreshed, p.clock())
		if merged.AccessToken != "" && (merged.AccessTokenExpiresAt.IsZero() || merged.AccessTokenExpiresAt.After(p.clock().Add(time.Minute))) {
			state.account = merged
			return merged, nil
		}
		return Account{}, ErrNoAvailableAccount
	}
	state.account = refreshed
	return refreshed, nil
}
