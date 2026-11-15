package credentials

import "weave-os/router/internal/proxy/usage"

// SubscriptionUsage is the observed rate-limit state of subscription
// credentials, keyed so the raw token never leaves the observer.
type SubscriptionUsage interface {
	Key(token []byte) usage.CredentialKey
	Snapshot(key usage.CredentialKey) (usage.Snapshot, bool)
}

// ClaudeSubscriptionFacts are the request facts that keep an observed Claude
// subscription serving through exhaustion or overage.
type ClaudeSubscriptionFacts struct {
	// EnterpriseOnly: a verified Enterprise seat funds its own overage.
	EnterpriseOnly bool
	// PickedModelOnOwnPlan: the provider, not the router, decides a picked
	// model on the caller's own login, as the native client would.
	PickedModelOnOwnPlan bool
	// EnterpriseSubscriptionOnly: with paid fallback off, a usage-billed
	// Enterprise seat has no plan quota to exhaust, so its overage is the
	// caller's only lane. Linked-first subscription-only is not this case.
	EnterpriseSubscriptionOnly bool
}

// ClaudeSnapshotUnservable reports whether an observed Claude subscription
// should leave the turn. Any exemption set in f keeps the subscription serving
// regardless of the snapshot's billable or exhausted state.
func ClaudeSnapshotUnservable(f ClaudeSubscriptionFacts, snap usage.Snapshot) bool {
	if f.EnterpriseOnly || f.PickedModelOnOwnPlan || f.EnterpriseSubscriptionOnly {
		return false
	}
	return snap.BillableOrExhausted()
}

// ClaudeSubscriptionObservedExhausted reports whether the observer says the
// caller's Claude subscription token should not serve the turn, independent of
// whether a fallback key exists. An absent or unobserved token never is.
func ClaudeSubscriptionObservedExhausted(observed SubscriptionUsage, token string, f ClaudeSubscriptionFacts) bool {
	if observed == nil || token == "" {
		return false
	}
	snap, ok := observed.Snapshot(observed.Key([]byte(token)))
	return ok && ClaudeSnapshotUnservable(f, snap)
}
