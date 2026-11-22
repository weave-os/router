package credentials_test

import (
	"testing"
	"time"

	"weave-os/router/internal/credentials"
	"weave-os/router/internal/credentials/usage"
	"weave-os/router/internal/providers"

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

	noFallback := credentials.ClaudeSubscriptionFacts{NoPaidFallback: true}
	assert.False(t, credentials.ClaudeSnapshotUnservable(noFallback, overage), "own overage serves when nothing else can pay")
	assert.True(t, credentials.ClaudeSnapshotUnservable(noFallback, spentSnapshot()), "a spent seat without overage stays unservable")
}

func TestClaudePlanFromUsage(t *testing.T) {
	enterpriseShape := usage.Snapshot{RepresentativeClaim: usage.AnthropicClaimOverage, OverageInUse: true}
	planOverage := usage.Snapshot{Primary: usage.Window{UsedPercent: 1, WindowMinutes: 300}, RepresentativeClaim: usage.AnthropicClaimOverage, OverageInUse: true}
	observed := fakeUsage{"k:enterprise": enterpriseShape, "k:plan": planOverage, "k:fresh": {Primary: usage.Window{UsedPercent: 0.2, WindowMinutes: 300}}}
	unknown := providers.SubscriptionPlanUnknown

	assert.Equal(t, providers.SubscriptionPlanEnterprise, credentials.ClaudePlanFromUsage(unknown, observed, []byte("enterprise")))
	assert.Equal(t, unknown, credentials.ClaudePlanFromUsage(unknown, observed, []byte("plan")), "overage after a spent window is a personal plan")
	assert.Equal(t, unknown, credentials.ClaudePlanFromUsage(unknown, observed, []byte("fresh")))
	assert.Equal(t, unknown, credentials.ClaudePlanFromUsage(unknown, observed, []byte("unseen")))
	assert.Equal(t, providers.SubscriptionPlanMax, credentials.ClaudePlanFromUsage(providers.SubscriptionPlanMax, observed, []byte("enterprise")),
		"a known plan outranks the header shape")
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
