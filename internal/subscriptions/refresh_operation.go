package subscriptions

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"
)

type RefreshReason string

const (
	RefreshReasonExpiry RefreshReason = "access_expiry"
	RefreshReasonAge    RefreshReason = "credential_age"
	RefreshReasonManual RefreshReason = "manual"
)

var errRefreshPending = errors.New("subscription refresh is in progress")

type refreshOperation struct {
	owner        auth.SubscriptionOwner
	account      Account
	afterVersion *int64
}

func (r *Runtime) refresh(owner auth.SubscriptionOwner) Refresher {
	return func(ctx context.Context, account Account) (Account, error) {
		return r.runRefresh(ctx, refreshOperation{owner: owner, account: account})
	}
}

func (r *Runtime) refreshSatisfied(op refreshOperation, credentials auth.SubscriptionCredentials) bool {
	if op.afterVersion != nil {
		return credentials.TokenRefreshVersion > *op.afterVersion && subscriptionAccessTokenUsable(credentials, r.clock())
	}
	return !r.refreshDue(op.account, credentials)
}

func (r *Runtime) runRefresh(ctx context.Context, op refreshOperation) (Account, error) {
	wait := refreshWaitInitial
	for attempt := 0; attempt < refreshRetryLimit; attempt++ {
		leaseID := uuid.NewString()
		acquired, err := r.acquireRefreshLease(ctx, op, leaseID)
		if err != nil {
			observability.FromContext(ctx).Error("Failed to acquire subscription refresh lease", "account_id", op.account.ID, "err", err)
			return Account{}, err
		}
		if acquired.Acquired {
			account, err := r.refreshAcquired(ctx, op, refreshHolder{leaseID: leaseID, tookOver: acquired.TookOver})
			if !errors.Is(err, errRefreshLeaseLost) {
				return account, err
			}
			continue
		}
		account, err := r.observeRefresh(ctx, op)
		if !errors.Is(err, errRefreshPending) {
			return account, err
		}
		if err := waitForRefresh(ctx, wait); err != nil {
			return Account{}, err
		}
		wait = min(wait*2, refreshWaitMaximum)
		attempt--
	}
	return Account{}, ErrNoAvailableAccount
}

func (r *Runtime) observeRefresh(ctx context.Context, op refreshOperation) (Account, error) {
	credentials, err := r.store.LoadSubscriptionCredentials(ctx, op.owner, op.account.ID)
	if err != nil {
		observability.FromContext(ctx).Error("Failed to load subscription credentials while waiting for refresh", "account_id", op.account.ID, "err", err)
		return Account{}, err
	}
	if !r.operationAvailable(ctx, op, credentials) {
		return Account{}, ErrNoAvailableAccount
	}
	if r.refreshSatisfied(op, credentials) {
		return r.cachedCredentials(op.account, credentials), nil
	}
	return Account{}, errRefreshPending
}

func (r *Runtime) refreshAcquired(ctx context.Context, op refreshOperation, holder refreshHolder) (Account, error) {
	credentials, err := r.store.LoadSubscriptionCredentials(ctx, op.owner, op.account.ID)
	if err != nil {
		observability.FromContext(ctx).Error("Failed to load subscription credentials for refresh", "account_id", op.account.ID, "err", err)
		return Account{}, r.releaseRefreshLease(ctx, op.owner, op.account.ID, holder.leaseID, err)
	}
	if !r.operationAvailable(ctx, op, credentials) {
		return Account{}, r.releaseRefreshLease(ctx, op.owner, op.account.ID, holder.leaseID, ErrNoAvailableAccount)
	}
	if r.refreshSatisfied(op, credentials) {
		return r.cachedCredentials(op.account, credentials), r.releaseRefreshLease(ctx, op.owner, op.account.ID, holder.leaseID, nil)
	}
	return r.exchangeAndPersist(ctx, op, holder, credentials)
}

func (r *Runtime) refreshReason(op refreshOperation, credentials auth.SubscriptionCredentials) RefreshReason {
	if op.afterVersion != nil {
		return RefreshReasonManual
	}
	if subscriptionAccessTokenUsable(credentials, r.clock()) {
		return RefreshReasonAge
	}
	return RefreshReasonExpiry
}

func refreshIdentityChanged(account Account, credentials auth.SubscriptionCredentials, refreshed RefreshedToken) bool {
	return account.Provider == ProviderCodex && ((refreshed.AccountID != "" && refreshed.AccountID != account.AccountID) ||
		(credentials.ProviderUserID != "" && ((refreshed.UserID != "" && refreshed.UserID != credentials.ProviderUserID) || refreshed.AccountID != account.AccountID)))
}

func (r *Runtime) exchangeAndPersist(ctx context.Context, op refreshOperation, holder refreshHolder, credentials auth.SubscriptionCredentials) (Account, error) {
	refreshed, err := r.refreshWithHeartbeat(ctx, op.owner, op.account, holder.leaseID, string(credentials.RefreshToken))
	if err != nil {
		return r.operationRefreshError(ctx, op, holder, credentials, err)
	}
	if refreshIdentityChanged(op.account, credentials, refreshed) {
		return r.operationRefreshError(ctx, op, holder, credentials, &providerAccountMismatchError{})
	}
	err = r.persistRefreshedTokens(ctx, op.owner, op.account, holder, credentials.TokenRefreshVersion, refreshed)
	if errors.Is(err, auth.ErrSubscriptionRefreshConflict) {
		if releaseErr := r.releaseRefreshLease(ctx, op.owner, op.account.ID, holder.leaseID, nil); releaseErr != nil {
			return Account{}, releaseErr
		}
		account, loadErr := r.observeRefresh(ctx, op)
		if errors.Is(loadErr, errRefreshPending) {
			return Account{}, errRefreshLeaseLost
		}
		return account, loadErr
	}
	if err != nil {
		observability.FromContext(ctx).Error("Failed to persist refreshed subscription credentials", "account_id", op.account.ID, "err", err)
		return Account{}, r.releaseRefreshLease(ctx, op.owner, op.account.ID, holder.leaseID, err)
	}
	account := op.account
	account.AccessToken, account.AccessTokenExpiresAt = refreshed.AccessToken, refreshed.ExpiresAt
	if refreshed.AccountID != "" {
		account.AccountID = refreshed.AccountID
	}
	refreshedAt := r.clock()
	account.AccessTokenExpiresAt = r.earlyRefresh.deadline(account, account.AccessToken, refreshed.ExpiresAt, &refreshedAt)
	observability.FromContext(ctx).Info("Subscription credentials refreshed", "account_id", account.ID, "provider", account.Provider,
		"reason", r.refreshReason(op, credentials), "token_refresh_version", credentials.TokenRefreshVersion+1,
		"issued_at", accessTokenIssuedAt(refreshed.AccessToken), "refreshed_at", refreshedAt,
		"expires_at", refreshed.ExpiresAt, "next_refresh_at", account.AccessTokenExpiresAt, "lease_taken_over", holder.tookOver)
	return account, nil
}

func (r *Runtime) operationRefreshError(ctx context.Context, op refreshOperation, holder refreshHolder, credentials auth.SubscriptionCredentials, refreshErr error) (Account, error) {
	if op.afterVersion != nil {
		return r.manualRefreshError(ctx, op, holder, refreshErr)
	}
	var terminal terminalRefreshError
	proactive := r.refreshReason(op, credentials) == RefreshReasonAge
	if proactive && ctx.Err() == nil && subscriptionAccessTokenUsable(credentials, r.clock()) &&
		(!errors.As(refreshErr, &terminal) || !terminal.Terminal()) {
		observability.FromContext(ctx).Warn("Early subscription refresh failed; retaining usable access", "account_id", op.account.ID, "err", refreshErr)
		account := applySubscriptionCredentials(op.account, credentials)
		retryAt := r.clock().Add(5 * time.Minute)
		if account.AccessTokenExpiresAt.IsZero() || retryAt.Before(account.AccessTokenExpiresAt) {
			account.AccessTokenExpiresAt = retryAt
		}
		return account, r.releaseRefreshLease(ctx, op.owner, account.ID, holder.leaseID, nil)
	}
	_, err := r.handleRefreshError(ctx, op.owner, op.account, credentials, holder, refreshErr)
	if err != nil {
		return Account{}, err
	}
	account, err := r.observeRefresh(ctx, op)
	if errors.Is(err, errRefreshPending) {
		return Account{}, errRefreshLeaseLost
	}
	return account, err
}

func (r *Runtime) acquireRefreshLease(ctx context.Context, op refreshOperation, leaseID string) (auth.RefreshLeaseAcquisition, error) {
	if op.afterVersion != nil {
		return r.store.TryAcquireSubscriptionMaintenanceRefreshLease(ctx, op.owner, op.account.ID, leaseID, r.leaseTTL)
	}
	return r.store.TryAcquireSubscriptionRefreshLease(ctx, op.owner, op.account.ID, leaseID, r.leaseTTL)
}
func (r *Runtime) operationAvailable(ctx context.Context, op refreshOperation, credentials auth.SubscriptionCredentials) bool {
	if op.afterVersion != nil && credentials.State == auth.SubscriptionAccountStateExhausted {
		return credentials.Enabled
	}
	return subscriptionCredentialAvailable(ctx, op.account.Provider, credentials, r.clock())
}
