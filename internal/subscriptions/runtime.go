package subscriptions

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"workweave/router/internal/auth"
	"workweave/router/internal/observability"
)

const defaultAccountSyncTTL = 15 * time.Second

// AccountStore is the encrypted persistence boundary used by Runtime.
type AccountStore interface {
	ListSubscriptionAccounts(context.Context, string) ([]*auth.SubscriptionAccount, error)
	SubscriptionRefreshToken(context.Context, string, string) ([]byte, error)
	UpdateSubscriptionRefreshToken(context.Context, string, string, []byte) error
	UpdateSubscriptionAccountState(context.Context, string, string, bool, *time.Time) error
	UpdateSubscriptionAccountCooldown(context.Context, string, string, time.Time) error
}

// Lease is a short-lived provider credential. Release must be called exactly once.
type Lease struct {
	AccountID       string
	AccessToken     string
	ProviderAccount string
	release         func()
}

// Release returns this account to the concurrent selector.
func (l Lease) Release() {
	if l.release != nil {
		l.release()
	}
}

// Leaser supplies owner-scoped subscription credentials to proxy dispatch.
type Leaser interface {
	Lease(context.Context, string, Provider, string) (Lease, bool, error)
	Cooldown(context.Context, string, Provider, string, time.Time) error
	Disable(context.Context, string, Provider, string) error
}

// Runtime synchronizes encrypted accounts, refreshes tokens, and leases them
// from independent owner/provider pools.
type Runtime struct {
	store     AccountStore
	refresher TokenRefresher
	manager   *Manager
	clock     func() time.Time
	syncTTL   time.Duration

	mu       sync.Mutex
	syncedAt map[string]time.Time
	syncing  map[string]*runtimeSyncCall
}

type runtimeSyncCall struct {
	done    chan struct{}
	present bool
	err     error
}

// NewRuntime constructs the server-side subscription credential runtime.
func NewRuntime(store AccountStore, refresher TokenRefresher, clock func() time.Time) *Runtime {
	if clock == nil {
		clock = time.Now
	}
	return &Runtime{
		store: store, refresher: refresher, manager: NewManager(clock), clock: clock,
		syncTTL: defaultAccountSyncTTL, syncedAt: make(map[string]time.Time), syncing: make(map[string]*runtimeSyncCall),
	}
}

func (r *Runtime) Lease(ctx context.Context, ownerID string, provider Provider, sessionID string) (Lease, bool, error) {
	if ownerID == "" || (provider != ProviderClaude && provider != ProviderCodex) {
		return Lease{}, false, nil
	}
	present, err := r.syncAccounts(ctx, ownerID, provider)
	if err != nil || !present {
		return Lease{}, present, err
	}
	account, release, err := r.manager.Lease(ctx, ownerID, provider, sessionID, r.refresh(ownerID))
	if err != nil {
		return Lease{}, true, err
	}
	return Lease{
		AccountID: account.ID, AccessToken: account.AccessToken,
		ProviderAccount: account.AccountID, release: release,
	}, true, nil
}

func (r *Runtime) Cooldown(ctx context.Context, ownerID string, provider Provider, accountID string, resetAt time.Time) error {
	if !r.manager.Cooldown(ownerID, provider, accountID, resetAt) {
		return ErrNoAvailableAccount
	}
	return r.store.UpdateSubscriptionAccountCooldown(ctx, ownerID, accountID, resetAt)
}

func (r *Runtime) Disable(ctx context.Context, ownerID string, provider Provider, accountID string) error {
	if !r.manager.Disable(ownerID, provider, accountID) {
		return ErrNoAvailableAccount
	}
	return r.store.UpdateSubscriptionAccountState(ctx, ownerID, accountID, false, nil)
}

func (r *Runtime) syncAccounts(ctx context.Context, ownerID string, provider Provider) (bool, error) {
	key := poolKey(ownerID, provider)
	r.mu.Lock()
	if syncedAt := r.syncedAt[key]; !syncedAt.IsZero() && r.clock().Sub(syncedAt) < r.syncTTL {
		r.mu.Unlock()
		return r.providerAccountCount(ownerID, provider) > 0, nil
	}
	if call, ok := r.syncing[key]; ok {
		r.mu.Unlock()
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-call.done:
			return call.present, call.err
		}
	}
	call := &runtimeSyncCall{done: make(chan struct{})}
	r.syncing[key] = call
	r.mu.Unlock()

	accounts, err := r.store.ListSubscriptionAccounts(ctx, ownerID)
	providerAccounts := make([]Account, 0, len(accounts))
	if err == nil {
		for _, account := range accounts {
			if Provider(account.Provider) != provider {
				continue
			}
			providerAccountID := ""
			if provider == ProviderCodex {
				providerAccountID = account.ExternalAccountID
			}
			var cooldown time.Time
			if account.CooldownUntil != nil {
				cooldown = *account.CooldownUntil
			}
			providerAccounts = append(providerAccounts, Account{
				ID: account.ID, OwnerID: ownerID, Provider: provider, AccountID: providerAccountID,
				Enabled: account.Enabled, CooldownTil: cooldown,
			})
		}
		err = r.manager.Sync(ownerID, provider, providerAccounts)
	}

	r.mu.Lock()
	if err == nil {
		r.syncedAt[key] = r.clock()
	}
	call.present, call.err = len(providerAccounts) > 0, err
	delete(r.syncing, key)
	close(call.done)
	r.mu.Unlock()
	return call.present, call.err
}

func (r *Runtime) providerAccountCount(ownerID string, provider Provider) int {
	return r.manager.pool(ownerID, provider).accountCount(provider)
}

func (r *Runtime) refresh(ownerID string) Refresher {
	return func(ctx context.Context, account Account) (Account, error) {
		refreshToken, err := r.store.SubscriptionRefreshToken(ctx, ownerID, account.ID)
		if err != nil {
			return Account{}, err
		}
		refreshed, err := r.refresher.Refresh(ctx, account.Provider, string(refreshToken))
		if err != nil {
			return Account{}, r.recordRefreshFailure(ctx, ownerID, account.ID, account.Provider, err)
		}
		if account.Provider == ProviderCodex && refreshed.AccountID != "" && refreshed.AccountID != account.AccountID {
			mismatch := &providerAccountMismatchError{}
			return Account{}, r.recordRefreshFailure(ctx, ownerID, account.ID, account.Provider, mismatch)
		}
		if refreshed.RefreshToken != string(refreshToken) {
			if err := r.store.UpdateSubscriptionRefreshToken(ctx, ownerID, account.ID, []byte(refreshed.RefreshToken)); err != nil {
				return Account{}, fmt.Errorf("persist rotated subscription refresh token: %w", err)
			}
		}
		account.AccessToken = refreshed.AccessToken
		account.AccessTokenExpiresAt = refreshed.ExpiresAt
		if refreshed.AccountID != "" {
			account.AccountID = refreshed.AccountID
		}
		account.CooldownTil = time.Time{}
		return account, nil
	}
}

func (r *Runtime) recordRefreshFailure(ctx context.Context, ownerID, accountID string, provider Provider, refreshErr error) error {
	if errors.Is(refreshErr, context.Canceled) || errors.Is(refreshErr, context.DeadlineExceeded) {
		return refreshErr
	}
	var terminal interface{ Terminal() bool }
	if errors.As(refreshErr, &terminal) && terminal.Terminal() {
		if err := r.store.UpdateSubscriptionAccountState(ctx, ownerID, accountID, false, nil); err != nil {
			observability.FromContext(ctx).Error("Failed to persist disabled subscription account after token rejection",
				"provider", provider, "account_id", accountID, "err", err)
			return errors.Join(refreshErr, fmt.Errorf("persist disabled subscription account state: %w", err))
		}
		return refreshErr
	}
	cooldownUntil := r.clock().Add(time.Minute)
	if err := r.store.UpdateSubscriptionAccountCooldown(ctx, ownerID, accountID, cooldownUntil); err != nil {
		observability.FromContext(ctx).Error("Failed to persist subscription account cooldown after token refresh failure",
			"provider", provider, "account_id", accountID, "err", err)
		return errors.Join(refreshErr, fmt.Errorf("persist subscription account cooldown: %w", err))
	}
	return refreshErr
}

type providerAccountMismatchError struct{}

func (*providerAccountMismatchError) Error() string {
	return "refreshed subscription account identity changed"
}
func (*providerAccountMismatchError) Terminal() bool { return true }
