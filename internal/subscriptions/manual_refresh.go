package subscriptions

import (
	"context"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"
)

type AccountRefreshResult struct {
	TokenRefreshedAt     *time.Time `json:"token_refreshed_at,omitempty"`
	AccountID            string     `json:"account_id"`
	Provider             Provider   `json:"provider"`
	TokenRefreshVersion  int64      `json:"token_refresh_version"`
	AccessTokenExpiresAt *time.Time `json:"access_token_expires_at,omitempty"`
	NextRefreshAt        time.Time  `json:"next_refresh_at"`
	QuotaReactivated     bool       `json:"quota_reactivated,omitempty"`
}

type AccountRefresher interface {
	RefreshAccount(context.Context, auth.SubscriptionOwner, string) (AccountRefreshResult, error)
}

func (r *Runtime) RefreshAccount(ctx context.Context, owner auth.SubscriptionOwner, accountID string) (AccountRefreshResult, error) {
	accounts, err := r.store.ListSubscriptionAccounts(ctx, owner)
	if err != nil {
		return AccountRefreshResult{}, err
	}
	for _, seat := range accounts {
		if seat.ID != accountID || seat.SubscriberID == "" || seat.SubscriberID != owner.SubscriberID {
			continue
		}
		account := accountFromSubscription(seat)
		return r.refreshOwnedAccount(ctx, owner, account)
	}
	return AccountRefreshResult{}, auth.ErrSubscriptionAccountNotFound
}

func (r *Runtime) refreshOwnedAccount(ctx context.Context, owner auth.SubscriptionOwner, account Account) (AccountRefreshResult, error) {
	before, err := r.store.LoadSubscriptionCredentials(ctx, owner, account.ID)
	if err != nil {
		return AccountRefreshResult{}, err
	}
	_, err = r.runRefresh(ctx, refreshOperation{owner: owner, account: account, afterVersion: &before.TokenRefreshVersion})
	if err != nil {
		return AccountRefreshResult{}, err
	}
	r.manager.InvalidateAccessToken(account.OwnerID, account.Provider, account.ID)
	after, err := r.store.LoadSubscriptionCredentials(ctx, owner, account.ID)
	if err != nil {
		return AccountRefreshResult{}, err
	}
	return AccountRefreshResult{AccountID: account.ID, Provider: account.Provider,
		TokenRefreshVersion: after.TokenRefreshVersion, AccessTokenExpiresAt: after.AccessTokenExpiresAt,
		TokenRefreshedAt: after.TokenRefreshedAt, NextRefreshAt: r.cachedCredentials(account, after).AccessTokenExpiresAt,
		QuotaReactivated: r.reactivateRestoredQuota(ctx, owner, account, after)}, nil
}

func (r *Runtime) manualRefreshError(ctx context.Context, op refreshOperation, holder refreshHolder, refreshErr error) (Account, error) {
	account, reloadErr := r.observeRefresh(ctx, op)
	if reloadErr == nil {
		return account, r.releaseRefreshLease(ctx, op.owner, op.account.ID, holder.leaseID, nil)
	}
	observability.FromContext(ctx).Warn("Requested subscription credential refresh failed", "account_id", op.account.ID,
		"provider", op.account.Provider, "reason", RefreshReasonManual, "token_refresh_version", *op.afterVersion, "err", refreshErr)
	return Account{}, r.releaseRefreshLease(ctx, op.owner, op.account.ID, holder.leaseID, refreshErr)
}
