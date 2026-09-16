package subscriptions

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"

	"github.com/google/uuid"
)

const (
	defaultAccountSyncTTL = 15 * time.Second
	refreshLeaseTTL       = 30 * time.Second
	refreshWaitInitial    = 50 * time.Millisecond
	refreshWaitMaximum    = 200 * time.Millisecond
	refreshRetryLimit     = 3
)

var errRefreshLeaseLost = errors.New("subscription refresh lease lost")

// AccountStore is the encrypted persistence boundary used by Runtime.
type AccountStore interface {
	ListSubscriptionAccounts(context.Context, string) ([]*auth.SubscriptionAccount, error)
	UpdateSubscriptionAccountState(context.Context, string, string, bool, *time.Time) error
	UpdateSubscriptionAccountCooldown(context.Context, string, string, time.Time) error
	TryAcquireSubscriptionRefreshLease(context.Context, string, string, string, time.Time, time.Time) (bool, error)
	ReleaseSubscriptionRefreshLease(context.Context, string, string, string) error
	LoadSubscriptionCredentials(context.Context, string, string) (auth.SubscriptionCredentials, error)
	PersistSubscriptionTokens(context.Context, string, string, string, int64, []byte, []byte, time.Time) error
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
		wait := refreshWaitInitial
		for attempt := 0; attempt < refreshRetryLimit; attempt++ {
			leaseID := uuid.NewString()
			now := r.clock()
			leaseUntil := now.Add(refreshLeaseTTL)
			acquired, err := r.store.TryAcquireSubscriptionRefreshLease(ctx, ownerID, account.ID, leaseID, now, leaseUntil)
			if err != nil {
				observability.FromContext(ctx).Error("Failed to acquire subscription refresh lease", "owner_id", ownerID, "account_id", account.ID, "provider", account.Provider, "err", err)
				return Account{}, err
			}
			if !acquired {
				credentials, loadErr := r.store.LoadSubscriptionCredentials(ctx, ownerID, account.ID)
				if loadErr != nil {
					observability.FromContext(ctx).Error("Failed to load subscription credentials while waiting for refresh", "owner_id", ownerID, "account_id", account.ID, "provider", account.Provider, "err", loadErr)
					return Account{}, loadErr
				}
				if !subscriptionCredentialAvailable(credentials, r.clock()) {
					return Account{}, ErrNoAvailableAccount
				}
				if subscriptionAccessTokenUsable(credentials, r.clock()) {
					return applySubscriptionCredentials(account, credentials), nil
				}
				if err := waitForRefresh(ctx, wait); err != nil {
					return Account{}, err
				}
				if wait < refreshWaitMaximum {
					wait *= 2
					if wait > refreshWaitMaximum {
						wait = refreshWaitMaximum
					}
				}
				attempt--
				continue
			}

			credentials, err := r.store.LoadSubscriptionCredentials(ctx, ownerID, account.ID)
			if err != nil {
				observability.FromContext(ctx).Error("Failed to load subscription credentials for refresh", "owner_id", ownerID, "account_id", account.ID, "provider", account.Provider, "err", err)
				return Account{}, r.releaseRefreshLease(ctx, ownerID, account.ID, leaseID, err)
			}
			if !subscriptionCredentialAvailable(credentials, r.clock()) {
				return Account{}, r.releaseRefreshLease(ctx, ownerID, account.ID, leaseID, ErrNoAvailableAccount)
			}
			if subscriptionAccessTokenUsable(credentials, r.clock()) {
				return applySubscriptionCredentials(account, credentials), r.releaseRefreshLease(ctx, ownerID, account.ID, leaseID, nil)
			}

			refreshed, refreshErr := r.refresher.Refresh(ctx, account.Provider, string(credentials.RefreshToken))
			if refreshErr != nil {
				recovered, handleErr := r.handleRefreshError(ctx, ownerID, account, credentials, leaseID, refreshErr)
				if errors.Is(handleErr, errRefreshLeaseLost) {
					continue
				}
				return recovered, handleErr
			}
			if account.Provider == ProviderCodex && refreshed.AccountID != "" && refreshed.AccountID != account.AccountID {
				mismatch := &providerAccountMismatchError{}
				recovered, handleErr := r.handleRefreshError(ctx, ownerID, account, credentials, leaseID, mismatch)
				if errors.Is(handleErr, errRefreshLeaseLost) {
					continue
				}
				return recovered, handleErr
			}
			persistErr := r.store.PersistSubscriptionTokens(ctx, ownerID, account.ID, leaseID, credentials.TokenRefreshVersion,
				[]byte(refreshed.RefreshToken), []byte(refreshed.AccessToken), refreshed.ExpiresAt)
			if errors.Is(persistErr, auth.ErrSubscriptionRefreshConflict) {
				latest, loadErr := r.store.LoadSubscriptionCredentials(ctx, ownerID, account.ID)
				if loadErr == nil && subscriptionCredentialAvailable(latest, r.clock()) && subscriptionAccessTokenUsable(latest, r.clock()) {
					return applySubscriptionCredentials(account, latest), nil
				}
				if loadErr != nil {
					persistErr = errors.Join(persistErr, loadErr)
				}
				if err := waitForRefresh(ctx, wait); err != nil {
					return Account{}, err
				}
				continue
			}
			if persistErr != nil {
				observability.FromContext(ctx).Error("Failed to persist refreshed subscription credentials", "owner_id", ownerID, "account_id", account.ID, "provider", account.Provider, "err", persistErr)
				return Account{}, r.releaseRefreshLease(ctx, ownerID, account.ID, leaseID, persistErr)
			}
			account.AccessToken = refreshed.AccessToken
			account.AccessTokenExpiresAt = refreshed.ExpiresAt
			if refreshed.AccountID != "" {
				account.AccountID = refreshed.AccountID
			}
			account.CooldownTil = time.Time{}
			return account, nil
		}
		return Account{}, ErrNoAvailableAccount
	}
}

func (r *Runtime) handleRefreshError(ctx context.Context, ownerID string, account Account, credentials auth.SubscriptionCredentials, leaseID string, refreshErr error) (Account, error) {
	if errors.Is(refreshErr, context.Canceled) || errors.Is(refreshErr, context.DeadlineExceeded) {
		return Account{}, r.releaseRefreshLease(ctx, ownerID, account.ID, leaseID, refreshErr)
	}
	var terminal terminalRefreshError
	if errors.As(refreshErr, &terminal) && terminal.Terminal() {
		latest, loadErr := r.store.LoadSubscriptionCredentials(ctx, ownerID, account.ID)
		if loadErr == nil && (latest.TokenRefreshVersion != credentials.TokenRefreshVersion || !bytes.Equal(latest.RefreshToken, credentials.RefreshToken)) {
			if subscriptionCredentialAvailable(latest, r.clock()) && subscriptionAccessTokenUsable(latest, r.clock()) {
				return applySubscriptionCredentials(account, latest), nil
			}
			if releaseErr := r.store.ReleaseSubscriptionRefreshLease(ctx, ownerID, account.ID, leaseID); releaseErr != nil {
				return Account{}, errors.Join(errRefreshLeaseLost, releaseErr)
			}
			return Account{}, errRefreshLeaseLost
		}
		if loadErr != nil {
			refreshErr = errors.Join(refreshErr, loadErr)
		}
	}
	refreshErr = r.releaseRefreshLease(ctx, ownerID, account.ID, leaseID, refreshErr)
	return Account{}, r.recordRefreshFailure(ctx, ownerID, account.ID, account.Provider, refreshErr)
}

func (r *Runtime) releaseRefreshLease(ctx context.Context, ownerID, accountID, leaseID string, err error) error {
	if releaseErr := r.store.ReleaseSubscriptionRefreshLease(ctx, ownerID, accountID, leaseID); releaseErr != nil {
		observability.FromContext(ctx).Error("Failed to release subscription refresh lease", "owner_id", ownerID, "account_id", accountID, "err", releaseErr)
		return errors.Join(err, fmt.Errorf("release subscription refresh lease: %w", releaseErr))
	}
	return err
}

func subscriptionAccessTokenUsable(credentials auth.SubscriptionCredentials, now time.Time) bool {
	if len(credentials.AccessToken) == 0 {
		return false
	}
	return credentials.AccessTokenExpiresAt == nil || credentials.AccessTokenExpiresAt.IsZero() || credentials.AccessTokenExpiresAt.After(now.Add(time.Minute))
}

func subscriptionCredentialAvailable(credentials auth.SubscriptionCredentials, now time.Time) bool {
	return credentials.Enabled && (credentials.CooldownUntil == nil || !credentials.CooldownUntil.After(now))
}

func applySubscriptionCredentials(account Account, credentials auth.SubscriptionCredentials) Account {
	account.AccessToken = string(credentials.AccessToken)
	if credentials.AccessTokenExpiresAt == nil {
		account.AccessTokenExpiresAt = time.Time{}
	} else {
		account.AccessTokenExpiresAt = *credentials.AccessTokenExpiresAt
	}
	account.CooldownTil = time.Time{}
	return account
}

func waitForRefresh(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
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
