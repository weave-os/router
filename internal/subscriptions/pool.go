package subscriptions

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
)

type Provider string

const (
	ProviderClaude Provider = "claude"
	ProviderCodex  Provider = "codex"
)

var upstreamPools = map[string]Provider{
	providers.ProviderAnthropic: ProviderClaude,
	providers.ProviderOpenAI:    ProviderCodex,
}

func PoolForUpstream(upstream string) (Provider, bool) {
	pool, ok := upstreamPools[upstream]
	return pool, ok
}

type Account struct {
	ID                   string
	OwnerID              string
	Provider             Provider
	AccessToken          string
	AccessTokenExpiresAt time.Time
	TokenRefreshVersion  int64
	AccountID            string
	Enabled              bool
	State                auth.SubscriptionAccountState
	CooldownTil          time.Time
}

type Refresher func(context.Context, Account) (Account, error)

var ErrNoAvailableAccount = errors.New("no available subscription account")

var ErrProviderMismatch = errors.New("subscription account provider mismatch")

type Pool struct {
	mu       sync.Mutex
	ownerID  string
	provider Provider
	accounts map[string]*accountState
	sticky   map[string]string
	refresh  map[string]*refreshCall
	clock    func() time.Time
}

type accountState struct {
	account Account
	leased  int
}

type refreshCall struct {
	done chan struct{}
	acct Account
	err  error
}

type terminalRefreshError interface {
	Terminal() bool
}

func NewPool(ownerID string, provider Provider, clock func() time.Time) *Pool {
	if clock == nil {
		clock = time.Now
	}
	return &Pool{
		ownerID:  ownerID,
		provider: provider,
		accounts: make(map[string]*accountState),
		sticky:   make(map[string]string),
		refresh:  make(map[string]*refreshCall),
		clock:    clock,
	}
}

func (p *Pool) Upsert(account Account) error {
	if account.ID == "" || account.OwnerID == "" {
		return errors.New("subscription account id and owner are required")
	}
	if account.OwnerID != p.ownerID || account.Provider != p.provider {
		return ErrProviderMismatch
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if existing, ok := p.accounts[account.ID]; ok {
		if existing.account.Provider != account.Provider || existing.account.OwnerID != account.OwnerID {
			return ErrProviderMismatch
		}
		quotaReactivated := account.TokenRefreshVersion > existing.account.TokenRefreshVersion &&
			existing.account.State == auth.SubscriptionAccountStateExhausted && account.State.Routable()
		if account.TokenRefreshVersion < existing.account.TokenRefreshVersion ||
			account.AccessToken == "" && account.TokenRefreshVersion == existing.account.TokenRefreshVersion {
			account.TokenRefreshVersion = existing.account.TokenRefreshVersion
			account.AccessToken = existing.account.AccessToken
			account.AccessTokenExpiresAt = existing.account.AccessTokenExpiresAt
		}
		if existing.account.State == auth.SubscriptionAccountStateActive && account.State == auth.SubscriptionAccountStateUnknown {
			account.State = existing.account.State
		}
		if !quotaReactivated && (existing.account.CooldownTil.After(account.CooldownTil) ||
			existing.account.State == auth.SubscriptionAccountStateCooldown && account.State == auth.SubscriptionAccountStateExhausted && existing.account.CooldownTil.After(p.clock())) {
			if existing.account.CooldownTil.After(account.CooldownTil) {
				account.CooldownTil = existing.account.CooldownTil
			}
			account.State = existing.account.State
		}
		existing.account = account
		return nil
	}
	p.accounts[account.ID] = &accountState{account: account}
	return nil
}

func (p *Pool) Sync(accounts []Account) error {
	present := make(map[string]struct{}, len(accounts))
	for _, account := range accounts {
		if err := p.Upsert(account); err != nil {
			return err
		}
		present[account.ID] = struct{}{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for accountID := range p.accounts {
		if _, ok := present[accountID]; !ok {
			delete(p.accounts, accountID)
		}
	}
	for sessionID, accountID := range p.sticky {
		if _, ok := present[accountID]; !ok {
			delete(p.sticky, sessionID)
		}
	}
	return nil
}

func (p *Pool) Disable(accountID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	state, ok := p.accounts[accountID]
	if ok {
		state.account.Enabled = false
		state.account.State = auth.SubscriptionAccountStateDisabled
	}
	return ok
}

func (p *Pool) Cooldown(accountID string, resetAt time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	state, ok := p.accounts[accountID]
	if !ok {
		return false
	}
	if state.account.CooldownTil.Before(resetAt) {
		state.account.CooldownTil = resetAt
	}
	state.account.State = auth.SubscriptionAccountStateCooldown
	return true
}

func (p *Pool) Exhaust(accountID string, resetAt time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	state, ok := p.accounts[accountID]
	if !ok || !state.account.Enabled {
		return false
	}
	if state.account.CooldownTil.Before(resetAt) {
		state.account.CooldownTil = resetAt
	}
	state.account.State = auth.SubscriptionAccountStateExhausted
	return true
}

func (p *Pool) ReconnectRequired(accountID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	state, ok := p.accounts[accountID]
	if !ok {
		return false
	}
	state.account.Enabled = false
	state.account.State = auth.SubscriptionAccountStateReconnectRequired
	state.account.CooldownTil = time.Time{}
	return true
}

func (p *Pool) InvalidateAccessToken(accountID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	state, ok := p.accounts[accountID]
	if !ok {
		return false
	}
	state.account.AccessToken = ""
	state.account.AccessTokenExpiresAt = time.Time{}
	return true
}

func (p *Pool) Activate(accountID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	state, ok := p.accounts[accountID]
	if !ok || !state.account.Enabled {
		return false
	}
	if state.account.CooldownTil.After(p.clock()) {
		return true
	}
	state.account.State = auth.SubscriptionAccountStateActive
	state.account.CooldownTil = time.Time{}
	return true
}

func (p *Pool) Remove(accountID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.accounts[accountID]; !ok {
		return false
	}
	delete(p.accounts, accountID)
	for sessionID, stickyID := range p.sticky {
		if stickyID == accountID {
			delete(p.sticky, sessionID)
		}
	}
	return true
}

func (p *Pool) Lease(ctx context.Context, provider Provider, sessionID string, refresh Refresher) (Account, func(), error) {
	if provider != p.provider {
		return Account{}, nil, ErrProviderMismatch
	}
	for attempts := 0; attempts < p.accountCount(provider); attempts++ {
		account, release, err := p.tryLease(ctx, provider, sessionID)
		if err != nil {
			return Account{}, nil, err
		}
		accessTokenUsable := account.AccessToken != "" &&
			(account.AccessTokenExpiresAt.IsZero() || account.AccessTokenExpiresAt.After(p.clock().Add(time.Minute)))
		if accessTokenUsable || refresh == nil {
			return account, release, nil
		}
		refreshed, err := p.refreshAccount(ctx, account, refresh)
		if err == nil {
			if ctx.Err() != nil {
				release()
				return Account{}, nil, ctx.Err()
			}
			return refreshed, release, nil
		}
		release()
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return Account{}, nil, err
		}
	}
	return Account{}, nil, ErrNoAvailableAccount
}

func (p *Pool) accountCount(provider Provider) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	count := 0
	for _, state := range p.accounts {
		if state.account.Provider == provider {
			count++
		}
	}
	return count
}

func (p *Pool) tryLease(ctx context.Context, provider Provider, sessionID string) (Account, func(), error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.clock()
	ids := make([]string, 0, len(p.accounts))
	for id, state := range p.accounts {
		if state.account.Provider == provider && state.account.Enabled &&
			subscriptionAccountEligible(ctx, provider, state.account.State, state.account.CooldownTil, now) {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return Account{}, nil, ErrNoAvailableAccount
	}
	sort.Slice(ids, func(i, j int) bool {
		left, right := p.accounts[ids[i]], p.accounts[ids[j]]
		if left.leased != right.leased {
			return left.leased < right.leased
		}
		return ids[i] < ids[j]
	})
	if stickyID := p.sticky[sessionID]; stickyID != "" {
		for _, id := range ids {
			if id == stickyID {
				ids = append([]string{id}, removeString(ids, id)...)
				break
			}
		}
	}
	state := p.accounts[ids[0]]
	state.leased++
	if sessionID != "" {
		p.sticky[sessionID] = state.account.ID
	}
	account := state.account
	var releaseOnce sync.Once
	return account, func() { releaseOnce.Do(func() { p.release(account.ID) }) }, nil
}

func subscriptionAccountEligible(ctx context.Context, provider Provider, state auth.SubscriptionAccountState, cooldownUntil, now time.Time) bool {
	if provider == ProviderClaude && requestcontext.SubscriptionExtraUsageFrom(ctx) && state == auth.SubscriptionAccountStateExhausted {
		return true
	}
	return subscriptionAccountStateRoutable(state, cooldownUntil, now)
}

func subscriptionAccountStateRoutable(state auth.SubscriptionAccountState, cooldownUntil, now time.Time) bool {
	if state == "" || state.Routable() {
		return !cooldownUntil.After(now)
	}
	return (state == auth.SubscriptionAccountStateExhausted || state == auth.SubscriptionAccountStateCooldown) &&
		!cooldownUntil.IsZero() &&
		!cooldownUntil.After(now)
}

func removeString(values []string, target string) []string {
	for i, value := range values {
		if value == target {
			return append(values[:i], values[i+1:]...)
		}
	}
	return values
}

func (p *Pool) release(accountID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if state, ok := p.accounts[accountID]; ok && state.leased > 0 {
		state.leased--
	}
}

func (p *Pool) refreshAccount(ctx context.Context, account Account, refresh Refresher) (Account, error) {
	p.mu.Lock()
	if state, ok := p.accounts[account.ID]; ok {
		current := state.account
		if current.AccessToken != "" && (current.AccessTokenExpiresAt.IsZero() || current.AccessTokenExpiresAt.After(p.clock().Add(time.Minute))) {
			p.mu.Unlock()
			return current, nil
		}
	}
	if call, ok := p.refresh[account.ID]; ok {
		p.mu.Unlock()
		select {
		case <-ctx.Done():
			return Account{}, ctx.Err()
		case <-call.done:
			return call.acct, call.err
		}
	}
	call := &refreshCall{done: make(chan struct{})}
	p.refresh[account.ID] = call
	p.mu.Unlock()

	go func() {
		refreshCtx, cancelRefresh := context.WithTimeout(context.WithoutCancel(ctx), refreshLeaseTTL)
		defer cancelRefresh()
		var refreshed Account
		var err error
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					observability.FromContext(refreshCtx).Error("Subscription refresh panicked", "account_id", account.ID, "panic_type", fmt.Sprintf("%T", recovered))
					err = errors.New("subscription refresh panicked")
				}
			}()
			refreshed, err = refresh(refreshCtx, account)
		}()
		var terminal terminalRefreshError
		var rejected *credentialRejectionCooldownError
		switch {
		case err == nil, errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		case errors.As(err, &rejected):
			p.Cooldown(account.ID, rejected.until)
		case errors.As(err, &terminal) && terminal.Terminal():
			p.ReconnectRequired(account.ID)
		default:
			p.Cooldown(account.ID, p.clock().Add(time.Minute))
		}
		p.mu.Lock()
		call.acct, call.err = refreshed, err
		delete(p.refresh, account.ID)
		close(call.done)
		if err == nil {
			if state, ok := p.accounts[account.ID]; ok {
				state.account = refreshed
			}
		}
		p.mu.Unlock()
	}()
	select {
	case <-ctx.Done():
		return Account{}, ctx.Err()
	case <-call.done:
		return call.acct, call.err
	}
}
