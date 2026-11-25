package credentials

import (
	"context"
	"time"

	"weave-os/router/internal/auth"
)

type ManagedSubscriptionCandidatesContextKey struct{}

type ManagedSubscriptionCandidate struct {
	AccountID          string
	Provider           auth.SubscriptionProvider
	State              auth.SubscriptionAccountState
	ResetAt            time.Time
	Enabled            bool
	CredentialRejected bool
	ExternalAccountID  string
	ProviderUserID     string
	UpdatedAt          time.Time
}

func WithManagedSubscriptionCandidates(ctx context.Context, accounts []*auth.SubscriptionAccount) context.Context {
	candidates := make([]ManagedSubscriptionCandidate, 0, len(accounts))
	enrolled := make(map[auth.SubscriptionProvider]struct{})
	for _, account := range accounts {
		candidate := ManagedSubscriptionCandidate{AccountID: account.ID, Provider: account.Provider, State: account.State, Enabled: account.Enabled, CredentialRejected: account.CredentialRejections > 0, ExternalAccountID: account.ExternalAccountID, ProviderUserID: account.ProviderUserID, UpdatedAt: account.UpdatedAt}
		if account.CooldownUntil != nil {
			candidate.ResetAt = *account.CooldownUntil
		}
		candidates = append(candidates, candidate)
		enrolled[account.Provider] = struct{}{}
	}
	ctx = context.WithValue(ctx, ManagedSubscriptionCandidatesContextKey{}, candidates)
	return context.WithValue(ctx, ManagedSubscriptionProvidersContextKey{}, enrolled)
}

func ManagedAccountThrottled(ctx context.Context, candidate ManagedSubscriptionCandidate, now time.Time) bool {
	if state, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage); state != nil {
		if state.ThrottledAccounts[candidate.AccountID].After(now) {
			return true
		}
	}
	return candidate.State == auth.SubscriptionAccountStateCooldown && candidate.ResetAt.After(now) && !candidate.CredentialRejected
}

func ManagedAccountCredentialCooling(candidate ManagedSubscriptionCandidate, now time.Time) bool {
	return candidate.State == auth.SubscriptionAccountStateCooldown && candidate.ResetAt.After(now) && candidate.CredentialRejected
}
