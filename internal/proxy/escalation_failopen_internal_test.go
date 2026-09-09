package proxy

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
)

func TestEscalationCommitFailurePreservesEligibilityAndTurnEvidence(t *testing.T) {
	store := newEscalationTestStore()
	observer := &escalationTestObserver{}
	role := roleForTier(catalog.TierFor("claude-opus-4-8"))
	pins := &rolePinStore{byRole: map[string]sessionpin.Pin{
		role: {Provider: providers.ProviderGoogle, Model: "gemini-3-pro-preview", Strategy: router.StrategyHMMEmbedding, PinnedUntil: time.Now().Add(time.Hour)},
	}}
	clients := map[string]providers.Client{providers.ProviderAnthropic: &stripFailureProvider{}, providers.ProviderGoogle: &stripFailureProvider{}}
	svc := NewService(nil, clients, nil, false, nil, pins, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil).
		WithEscalation(store, observer).
		WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyHMMEmbedding, Router: escalationDispatchRouter{}, Capabilities: policy.Capabilities{SchemaVersion: policy.SchemaVersionV1}})
	svc.scopedSearchRequirement = true
	svc.searchRequirementDecayTurns = DefaultSearchRequirementDecayTurns
	svc.searchUse = newSearchUseTracker()
	ctx := flags.WithOverrides(router.WithStrategy(context.Background(), router.StrategyHMMEmbedding), flags.Overrides{Bools: map[flags.Key]bool{
		flags.KeyEscalationXGBoostEnabled: true, flags.KeyPlannerEnabled: false, flags.KeyPrefixTrimFreeSwitch: true,
	}})
	installation := uuid.New()
	for n := 1; n <= 5; n++ {
		env := escalationTestEnvelope(t, n)
		feats := env.RoutingFeatures(false)
		sessionKey := deriveSessionKeyForRequest(ctx, env, "test-key")
		if n == 5 {
			store.failCommit = true
			svc.compaction.checkAndRecord(sessionKey, installation, role, 9, 0)
			require.True(t, svc.searchUse.observe(string(sessionKey[:]), 0, DefaultSearchRequirementDecayTurns))
		}
		res, err := svc.runTurnLoop(ctx, env, feats, "test-key", installation, "", http.Header{}, router.Request{
			RequestedModel:          feats.Model,
			TranslationRequirements: router.TranslationRequirements{SourceFormat: router.WireFormatAnthropic, Endpoint: router.EndpointAnthropicMessages, CitationsOrSearch: true},
		})
		require.NoError(t, err)
		if n < 5 {
			require.Equal(t, providers.ProviderGoogle, res.Decision.Provider)
			continue
		}
		require.Zero(t, res.EscalationOrdinal)
		require.Equal(t, providers.ProviderAnthropic, res.Decision.Provider, "native search eligibility must still exclude the Google pin during fallback")
		require.Equal(t, "claude-haiku-4-5", res.Decision.Model)
		require.False(t, res.StickyHit)
		require.True(t, res.PrefixTrimmed, "fallback must retain the current turn's compaction evidence")
		require.True(t, res.PrefixBroken)
		remaining, found := svc.searchUse.cache.Get(string(sessionKey[:]))
		require.True(t, found)
		require.Equal(t, DefaultSearchRequirementDecayTurns-1, remaining, "fallback must not age search evidence a second time")
	}
	require.Len(t, observer.requests, 5, "fallback must not observe the same turn twice")
}
