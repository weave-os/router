package proxy

import (
	"context"
	"testing"
	"time"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrecompactionPolicyReviewsEveryCascadeCandidate(t *testing.T) {
	spec, ok := policy.DefaultRegistry().Spec(policy.PurposePrecompactionSummary)
	require.True(t, ok)
	assert.Contains(t, spec.FixedCatalogModels, policy.PrecompactionDefaultModel)
	assert.Contains(t, spec.FixedCatalogModels, policy.PrecompactionLargeWindowModel)
	for _, m := range catalog.Models {
		anthropic := false
		for _, b := range m.Providers {
			anthropic = anthropic || b.Provider == providers.ProviderAnthropic
		}
		if anthropic && m.Tier != catalog.TierLow {
			assert.Contains(t, spec.FixedCatalogModels, m.ID, "warm-pin candidate %s must be a reviewed summarizer", m.ID)
		}
	}
}

// rolePinStore serves a distinct pin per role so the thread pin and the
// _hmm_history row can disagree.
type rolePinStore struct {
	stubPinStore
	byRole map[string]sessionpin.Pin
}

func (s *rolePinStore) Get(_ context.Context, _ [sessionpin.SessionKeyLen]byte, role string) (sessionpin.Pin, bool, error) {
	pin, found := s.byRole[role]
	return pin, found, nil
}

func TestCompactionSessionDecision(t *testing.T) {
	var key [sessionpin.SessionKeyLen]byte
	ctx := context.Background()
	live := time.Now().Add(time.Hour)
	openAIAndAnthropic := map[string]struct{}{providers.ProviderOpenAI: {}, providers.ProviderAnthropic: {}}
	history := func(pin sessionpin.Pin) *rolePinStore {
		return &rolePinStore{byRole: map[string]sessionpin.Pin{hmmHistoryRole(sessionpin.DefaultRole): pin}}
	}
	solHistory := history(sessionpin.Pin{Provider: providers.ProviderOpenAI, LastServedModel: "gpt-5.6-sol", LastTurnEndedAt: time.Now(), PinnedUntil: live})
	switched := &rolePinStore{byRole: map[string]sessionpin.Pin{
		sessionpin.DefaultRole:                 {Provider: providers.ProviderOpenAI, Model: "gpt-5.6-terra", LastServedModel: "gpt-5.6-terra", LastTurnEndedAt: time.Now().Add(-time.Minute), PinnedUntil: live},
		hmmHistoryRole(sessionpin.DefaultRole): {Provider: providers.ProviderOpenAI, LastServedModel: "gpt-5.6-sol", LastTurnEndedAt: time.Now(), PinnedUntil: live},
	}}
	expired := &rolePinStore{byRole: map[string]sessionpin.Pin{
		sessionpin.DefaultRole: {Provider: providers.ProviderOpenAI, Model: "gpt-5.6-sol", LastServedModel: "gpt-5.6-sol", LastTurnEndedAt: time.Now(), PinnedUntil: time.Now().Add(-time.Hour)},
	}}
	anthropicPinned := &rolePinStore{byRole: map[string]sessionpin.Pin{
		sessionpin.DefaultRole: {Provider: providers.ProviderAnthropic, Model: "claude-opus-4-8", LastServedModel: "claude-opus-4-8", LastTurnEndedAt: time.Now(), PinnedUntil: live},
	}}
	solFamily := map[string]struct{}{"gpt-5.6-sol": {}}
	for _, test := range []struct {
		name         string
		store        *rolePinStore
		available    map[string]struct{}
		req          router.Request
		wantModel    string
		wantEffort   string
		wantProvider string
	}{
		{name: "provider struck out this session", store: history(sessionpin.Pin{Provider: providers.ProviderOpenAI, LastServedModel: "gpt-5.6-sol", LastTurnEndedAt: time.Now(), DisabledProviders: []string{providers.ProviderOpenAI}})},
		{name: "model demoted this session", store: history(sessionpin.Pin{Provider: providers.ProviderOpenAI, LastServedModel: "gpt-5.6-sol", LastTurnEndedAt: time.Now(), DemotedModels: []string{"gpt-5.6-sol"}})},
		{name: "served model, not a newer family member", store: solHistory, wantModel: "gpt-5.6-sol", wantProvider: providers.ProviderOpenAI},
		{name: "latest served wins", store: switched, wantModel: "gpt-5.6-sol", wantProvider: providers.ProviderOpenAI},
		{name: "expired thread pin", store: expired},
		{name: "anthropic session", store: anthropicPinned, wantModel: "claude-opus-4-8", wantProvider: providers.ProviderAnthropic},
		{name: "low-tier session", store: history(sessionpin.Pin{Provider: providers.ProviderOpenAI, LastServedModel: "gpt-4.1-mini", LastTurnEndedAt: time.Now()}), wantModel: "gpt-4.1-mini", wantProvider: providers.ProviderOpenAI},
		{name: "effort qualified", store: history(sessionpin.Pin{Provider: providers.ProviderOpenAI, LastServedModel: "gpt-5.6-luna:xhigh", LastTurnEndedAt: time.Now()}), wantModel: "gpt-5.6-luna", wantEffort: "xhigh", wantProvider: providers.ProviderOpenAI},
		{name: "provider disabled", store: solHistory, req: router.Request{EnabledProviders: map[string]struct{}{providers.ProviderAnthropic: {}}}},
		{name: "org exclusion", store: solHistory, req: router.Request{ExcludedModels: solFamily}},
		{name: "automatic exclusion", store: solHistory, req: router.Request{AutomaticExcludedModels: solFamily}},
		{name: "unavailable in deployment", store: solHistory, available: map[string]struct{}{"gpt-6-sol": {}}},
		{name: "transcript past the window", store: solHistory, req: router.Request{EstimatedInputTokens: catalog.ContextWindowForBinding("gpt-5.6-sol", providers.ProviderOpenAI)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			session := compactionSession{pins: test.store, providers: openAIAndAnthropic, available: test.available, now: time.Now()}
			decision, _, ok := session.decision(ctx, key, sessionpin.DefaultRole, test.req)
			assert.Equal(t, test.wantModel != "", ok)
			assert.Equal(t, test.wantModel, decision.Model)
			assert.Equal(t, test.wantEffort, decision.Effort)
			assert.Equal(t, test.wantProvider, decision.Provider)
		})
	}
}

func TestTurnLoop_CompactionReadsClientIdentityBeforeSessionPin(t *testing.T) {
	const sessionModel = "gpt-6-sol"
	store := &rolePinStore{byRole: map[string]sessionpin.Pin{
		hmmHistoryRole(roleForTier(catalog.TierMid)): {
			Provider: providers.ProviderOpenAI, LastServedModel: sessionModel,
			LastTurnEndedAt: time.Now(), PinnedUntil: time.Now().Add(time.Hour),
		},
	}}
	s := NewService(nil, map[string]providers.Client{providers.ProviderOpenAI: nil, providers.ProviderAnthropic: nil}, nil, false, nil, store, false, providers.ProviderAnthropic, policy.PrecompactionDefaultModel, nil)
	env, err := translate.ParseOpenAI([]byte(`{"model":"gpt-5.6-luna","messages":[{"role":"user","content":"You are performing a CONTEXT CHECKPOINT COMPACTION. Create a summary."}],"max_tokens":4096}`))
	require.NoError(t, err)
	ctx := context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppCodex})
	features := env.RoutingFeatures(false)
	turn, err := s.runTurnLoop(ctx, env, features, "test-key", uuid.Nil, "", nil, router.Request{RequestedModel: features.Model})
	require.NoError(t, err)
	assert.Equal(t, turntype.Compaction, turn.TurnType)
	assert.Equal(t, providers.ProviderOpenAI, turn.Decision.Provider)
	assert.Equal(t, sessionModel, turn.Decision.Model)
}

func TestSessionStrikesWithdrawStruckArmsFromScoring(t *testing.T) {
	strikes := sessionStrikes{disabledProviders: []string{providers.ProviderOpenAI}, demotedModels: []string{"claude-opus-4-8"}}
	req := strikes.apply(router.Request{EnabledProviders: map[string]struct{}{providers.ProviderOpenAI: {}, providers.ProviderAnthropic: {}}})
	assert.Equal(t, map[string]struct{}{providers.ProviderAnthropic: {}}, req.EnabledProviders)
	assert.Contains(t, req.AutomaticExcludedModels, "claude-opus-4-8")
}
