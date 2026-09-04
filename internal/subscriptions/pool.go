// Package subscriptions provides concurrency-safe provider subscription pools.
package subscriptions

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

// Provider identifies the subscription family an account can serve.
type Provider string

const (
	// ProviderClaude is an Anthropic/Claude subscription.
	ProviderClaude Provider = "claude"
	// ProviderCodex is an OpenAI/Codex subscription.
	ProviderCodex Provider = "codex"
)

// Account is the non-secret state needed to select a subscription account.
// AccessToken is returned only from Lease and must never be persisted or logged.
type Account struct {
	ID                   string
	OwnerID              string
	Provider             Provider
	AccessToken          string
	AccessTokenExpiresAt time.Time
	AccountID            string
	Enabled              bool
	CooldownTil          time.Time
}

// Refresher obtains a fresh access token for an account. Implementations may
// rotate the refresh token and should return the replacement in Account.
type Refresher func(context.Context, Account) (Account, error)

// ErrNoAvailableAccount means every account in the provider pool is disabled,
// cooling down, or currently leased.
var ErrNoAvailableAccount = errors.New("no available subscription account")

// ErrProviderMismatch means an account was added to the wrong provider pool.
var ErrProviderMismatch = errors.New("subscription account provider mismatch")

// Pool selects accounts for one owner and provider. A Pool never shares
// accounts between owners or provider families.
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

// NewPool creates an empty pool for one owner and provider. clock is injectable
// for deterministic cooldown tests; nil uses time.Now.
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

// Upsert adds or replaces an account. The owner and provider are part of the
// pool identity and cannot be changed through this method.
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
		if account.AccessToken == "" {
			account.AccessToken = existing.account.AccessToken
			account.AccessTokenExpiresAt = existing.account.AccessTokenExpiresAt
		}
		existing.account = account
		return nil
	}
	p.accounts[account.ID] = &accountState{account: account}
	return nil
}

// Sync replaces persisted account metadata while retaining live access tokens
// and in-flight lease counts for accounts that still exist.
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

// Disable marks an account unavailable without deleting its identity.
func (p *Pool) Disable(accountID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	state, ok := p.accounts[accountID]
	if ok {
		state.account.Enabled = false
	}
	return ok
}

// Cooldown marks an account unavailable until resetAt after a quota response.
// It returns false when accountID is not part of this owner/provider pool.
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
	return true
}

// Remove deletes an account and any sticky session references to it.
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

// Lease reserves an account for one request. Release must be called exactly
// once, including when dispatch fails before any response bytes are sent.
func (p *Pool) Lease(ctx context.Context, provider Provider, sessionID string, refresh Refresher) (Account, func(), error) {
	if provider != p.provider {
		return Account{}, nil, ErrProviderMismatch
	}
	for attempts := 0; attempts < p.accountCount(provider); attempts++ {
		account, release, err := p.tryLease(provider, sessionID)
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
			return refreshed, release, nil
		}
		release()
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return Account{}, nil, err
		}
		var terminal terminalRefreshError
		if errors.As(err, &terminal) && terminal.Terminal() {
			p.Disable(account.ID)
			continue
		}
		p.Cooldown(account.ID, p.clock().Add(time.Minute))
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

func (p *Pool) tryLease(provider Provider, sessionID string) (Account, func(), error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.clock()
	ids := make([]string, 0, len(p.accounts))
	for id, state := range p.accounts {
		if state.account.Provider == provider && state.account.Enabled && !state.account.CooldownTil.After(now) {
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

	refreshed, err := refresh(ctx, account)
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
	return refreshed, err
}
