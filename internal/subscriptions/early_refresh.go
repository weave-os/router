package subscriptions

import (
	"errors"
	"maps"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"weave-os/router/internal/auth"
)

// EarlyRefreshPolicy shortens the cached lifetime for explicitly enrolled Codex
// seats. JWT issuance only schedules refresh; it never grants authorization.
type EarlyRefreshPolicy struct {
	accountIDs map[string]struct{}
	maxAge     time.Duration
}

func NewEarlyRefreshPolicy(accountIDs []string, maxAge time.Duration) (EarlyRefreshPolicy, error) {
	if len(accountIDs) == 0 || maxAge <= time.Minute {
		return EarlyRefreshPolicy{}, errors.New("early refresh requires explicit accounts and an age greater than one minute")
	}
	policy := EarlyRefreshPolicy{accountIDs: make(map[string]struct{}, len(accountIDs)), maxAge: maxAge}
	for _, id := range accountIDs {
		if id == "" {
			return EarlyRefreshPolicy{}, errors.New("early refresh account ID must not be empty")
		}
		policy.accountIDs[id] = struct{}{}
	}
	return policy, nil
}

func (r *Runtime) WithEarlyRefreshPolicy(policy EarlyRefreshPolicy) *Runtime {
	r.earlyRefresh = EarlyRefreshPolicy{accountIDs: maps.Clone(policy.accountIDs), maxAge: policy.maxAge}
	return r
}

func (p EarlyRefreshPolicy) deadline(account Account, accessToken string, expiry time.Time, refreshedAt *time.Time) time.Time {
	if _, enabled := p.accountIDs[account.ID]; !enabled || account.Provider != ProviderCodex {
		return expiry
	}
	issuedAt := refreshedAt
	if issuedAt == nil {
		issuedAt = accessTokenIssuedAt(accessToken)
	}
	if issuedAt == nil {
		return expiry
	}
	due := issuedAt.Add(p.maxAge)
	if expiry.IsZero() || due.Before(expiry) {
		return due
	}
	return expiry
}

func accessTokenIssuedAt(token string) *time.Time {
	claims := &jwt.RegisteredClaims{}
	if _, _, err := new(jwt.Parser).ParseUnverified(token, claims); err != nil || claims.IssuedAt == nil {
		return nil
	}
	issuedAt := claims.IssuedAt.UTC()
	return &issuedAt
}

func (r *Runtime) cachedCredentials(account Account, credentials auth.SubscriptionCredentials) Account {
	account = applySubscriptionCredentials(account, credentials)
	account.AccessTokenExpiresAt = r.earlyRefresh.deadline(account, account.AccessToken, account.AccessTokenExpiresAt, credentials.TokenRefreshedAt)
	return account
}

func (r *Runtime) refreshDue(account Account, credentials auth.SubscriptionCredentials) bool {
	cached := r.cachedCredentials(account, credentials)
	return cached.AccessToken == "" || !cached.AccessTokenExpiresAt.IsZero() &&
		!cached.AccessTokenExpiresAt.After(r.clock().Add(time.Minute))
}
