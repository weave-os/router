package credentials_test

import (
	"testing"
	"time"

	"weave-os/router/internal/credentials"
	"weave-os/router/internal/proxy/usage"

	"github.com/stretchr/testify/assert"
)

type fakeUsage map[usage.CredentialKey]usage.Snapshot

func (f fakeUsage) Key(token []byte) usage.CredentialKey {
	return usage.CredentialKey("k:" + string(token))
}

func (f fakeUsage) Snapshot(key usage.CredentialKey) (usage.Snapshot, bool) {
	snap, ok := f[key]
	return snap, ok
}

func spentSnapshot() usage.Snapshot {
	now := time.Now()
	return usage.Snapshot{
		Primary:    usage.Window{UsedPercent: 1, ResetAt: now.Add(time.Hour)},
		ObservedAt: now,
	}
}

func TestClaudeSnapshotUnservable(t *testing.T) {
	overage := usage.Snapshot{OverageInUse: true}
	assert.True(t, credentials.ClaudeSnapshotUnservable(credentials.ClaudeSubscriptionFacts{}, spentSnapshot()))
	assert.True(t, credentials.ClaudeSnapshotUnservable(credentials.ClaudeSubscriptionFacts{}, overage))
	assert.False(t, credentials.ClaudeSnapshotUnservable(credentials.ClaudeSubscriptionFacts{}, usage.Snapshot{}),
		"a snapshot with headroom keeps serving")

	for name, facts := range map[string]credentials.ClaudeSubscriptionFacts{
		"enterprise only":              {EnterpriseOnly: true},
		"picked model on own plan":     {PickedModelOnOwnPlan: true},
		"enterprise subscription only": {EnterpriseSubscriptionOnly: true},
	} {
		t.Run(name, func(t *testing.T) {
			assert.False(t, credentials.ClaudeSnapshotUnservable(facts, spentSnapshot()))
			assert.False(t, credentials.ClaudeSnapshotUnservable(facts, overage))
		})
	}

	ownOverageFirst := credentials.ClaudeSubscriptionFacts{OwnOverageBeforePool: true}
	assert.False(t, credentials.ClaudeSnapshotUnservable(ownOverageFirst, overage), "own overage serves before the pool")
	assert.True(t, credentials.ClaudeSnapshotUnservable(ownOverageFirst, spentSnapshot()), "a spent seat without overage stays unservable")
}

func TestClaudeSubscriptionObservedExhausted(t *testing.T) {
	observed := fakeUsage{"k:spent": spentSnapshot(), "k:fresh": {}}
	noFacts := credentials.ClaudeSubscriptionFacts{}

	assert.True(t, credentials.ClaudeSubscriptionObservedExhausted(observed, "spent", noFacts))
	assert.False(t, credentials.ClaudeSubscriptionObservedExhausted(observed, "fresh", noFacts))
	assert.False(t, credentials.ClaudeSubscriptionObservedExhausted(observed, "unseen", noFacts), "an unobserved token is not exhausted")
	assert.False(t, credentials.ClaudeSubscriptionObservedExhausted(observed, "", noFacts), "no subscription token present")
	assert.False(t, credentials.ClaudeSubscriptionObservedExhausted(nil, "spent", noFacts), "no observer wired")
	assert.False(t, credentials.ClaudeSubscriptionObservedExhausted(observed, "spent", credentials.ClaudeSubscriptionFacts{EnterpriseOnly: true}))
}
