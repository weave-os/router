package credentials

import "weave-os/router/internal/proxy/usage"

type SubscriptionUsage interface {
	Key(token []byte) usage.CredentialKey
	Snapshot(key usage.CredentialKey) (usage.Snapshot, bool)
}

type ClaudeSubscriptionFacts struct {
	EnterpriseOnly             bool
	PickedModelOnOwnPlan       bool
	OwnOverageBeforePool       bool
	EnterpriseSubscriptionOnly bool
}

func ClaudeSnapshotUnservable(f ClaudeSubscriptionFacts, snap usage.Snapshot) bool {
	if f.EnterpriseOnly || f.PickedModelOnOwnPlan || f.OwnOverageBeforePool && snap.OverageInUse || f.EnterpriseSubscriptionOnly {
		return false
	}
	return snap.BillableOrExhausted()
}

func ClaudeSubscriptionObservedExhausted(observed SubscriptionUsage, token string, f ClaudeSubscriptionFacts) bool {
	if observed == nil || token == "" {
		return false
	}
	snap, ok := observed.Snapshot(observed.Key([]byte(token)))
	return ok && ClaudeSnapshotUnservable(f, snap)
}
