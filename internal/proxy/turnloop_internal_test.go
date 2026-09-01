package proxy

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"workweave/router/internal/providers"
	"workweave/router/internal/router"
	"workweave/router/internal/router/catalog"
	"workweave/router/internal/router/planner"
	"workweave/router/internal/router/sessionpin"
)

// stubPinStore is a minimal sessionpin.Store for testing recordTurnUsage.
// getPin/getFound configure Get's response and default to a miss.
type stubPinStore struct {
	mu         sync.Mutex
	lastUsage  sessionpin.Usage
	usageHits  int
	usageRoles []string
	getPin     sessionpin.Pin
	getFound   bool
	consumePin sessionpin.Pin
	consumeHit bool
	consumeFor router.Strategy
	upserts    []sessionpin.Pin
	upsertErr  error
}

func newStubPinStore() *stubPinStore {
	return &stubPinStore{}
}

func (s *stubPinStore) Get(context.Context, [sessionpin.SessionKeyLen]byte, string) (sessionpin.Pin, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getPin, s.getFound, nil
}

func (s *stubPinStore) Upsert(_ context.Context, p sessionpin.Pin) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.upsertErr != nil {
		return s.upsertErr
	}
	s.upserts = append(s.upserts, p)
	return nil
}

func (s *stubPinStore) UpdateUsage(_ context.Context, _ [sessionpin.SessionKeyLen]byte, role string, u sessionpin.Usage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastUsage = u
	s.usageHits++
	s.usageRoles = append(s.usageRoles, role)
	return nil
}

func (s *stubPinStore) IncrementUpstreamErrors(context.Context, [sessionpin.SessionKeyLen]byte, string, router.Strategy) (int, error) {
	return 0, nil
}

func (s *stubPinStore) ResetUpstreamErrors(context.Context, [sessionpin.SessionKeyLen]byte, string, router.Strategy) error {
	return nil
}

func (s *stubPinStore) IncrementOverloadErrors(context.Context, [sessionpin.SessionKeyLen]byte, string, router.Strategy) (int, error) {
	return 0, nil
}

func (s *stubPinStore) ResetOverloadErrors(context.Context, [sessionpin.SessionKeyLen]byte, string, router.Strategy) error {
	return nil
}

func (s *stubPinStore) DisableProvider(context.Context, [sessionpin.SessionKeyLen]byte, string, string, router.Strategy) error {
	return nil
}

func (s *stubPinStore) Consume(_ context.Context, _ [sessionpin.SessionKeyLen]byte, _ string, expected router.Strategy) (sessionpin.Pin, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.consumeFor = expected
	return s.consumePin, s.consumeHit, nil
}

func (s *stubPinStore) SweepExpired(context.Context) error { return nil }

func TestPinCacheCold_OrdinaryAndHMMShareTheSameRule(t *testing.T) {
	t.Parallel()

	warm := sessionpin.Pin{
		Provider:        providers.ProviderAnthropic,
		LastTurnEndedAt: time.Now().Add(-time.Minute),
	}
	cold := warm
	cold.LastTurnEndedAt = time.Now().Add(-2 * time.Hour)

	assert.False(t, pinCacheCold(warm, false), "a warm unmodified prefix remains cache-eligible")
	assert.True(t, pinCacheCold(warm, true), "a prefix break makes a warm pin cold")
	assert.True(t, pinCacheCold(cold, false), "an expired pin is cold without a prefix break")
}

func TestConsumePostCommandContinuation_RequiresEffectiveStrategy(t *testing.T) {
	store := newStubPinStore()
	store.consumeHit = true
	store.consumePin = sessionpin.Pin{Strategy: router.StrategyCluster}
	svc := NewService(nil, nil, nil, false, nil, store, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil)
	ctx := router.WithStrategy(context.Background(), router.StrategyHMMBeta)

	_, found := svc.consumePostCommandContinuation(ctx, [sessionpin.SessionKeyLen]byte{1}, sessionpin.DefaultRole)
	assert.False(t, found, "a non-beta continuation must not cross into a beta session")

	store.mu.Lock()
	defer store.mu.Unlock()
	assert.Equal(t, router.StrategyHMMBeta, store.consumeFor,
		"the atomic consume must be filtered by the effective strategy in storage too")
}

func TestApplyPinEvidence_UsesAvailablePriorTurnData(t *testing.T) {
	t.Parallel()

	zeroTimestamp := turnLoopResult{}
	applyPinEvidence(&zeroTimestamp, sessionpin.Pin{
		Provider: providers.ProviderAnthropic,
		Model:    "claude-haiku-4-5",
	})
	assert.Equal(t, providers.ProviderAnthropic, zeroTimestamp.PinProvider)
	assert.Nil(t, zeroTimestamp.PriorTurnGapMS)

	withHistory := turnLoopResult{}
	applyPinEvidence(&withHistory, sessionpin.Pin{
		Provider:        providers.ProviderFireworks,
		Model:           "accounts/fireworks/models/qwen3-235b-a22b",
		LastTurnEndedAt: time.Now().Add(-time.Second),
	})
	assert.Equal(t, providers.ProviderFireworks, withHistory.PinProvider)
	require.NotNil(t, withHistory.PriorTurnGapMS)
	assert.Greater(t, *withHistory.PriorTurnGapMS, int64(0))

	clearPinEvidence(&withHistory)
	assert.Empty(t, withHistory.PinModel)
	assert.Empty(t, withHistory.PinProvider)
	assert.Zero(t, withHistory.PinAgeSec)
	assert.Nil(t, withHistory.PriorTurnGapMS)
}

func TestCacheablePrefixTokens_UsesReadOrWriteEvidence(t *testing.T) {
	t.Parallel()

	// Read and write both count as prior-turn cache evidence. The prior total
	// is 10k (compat basis: LastInputTokens already includes cached).
	writeOnly := sessionpin.Pin{LastInputTokens: 10_000, LastCachedWriteTokens: 4_000}
	got, known := cacheablePrefixTokens(writeOnly, 10_000, false)
	assert.True(t, known)
	assert.Equal(t, 4_000, got)

	// The share is scale-free: the same 0.4 on a 2k prompt is 800 tokens, not
	// the old min(evidence, total) clamp.
	readOnly := sessionpin.Pin{LastInputTokens: 10_000, LastCachedReadTokens: 4_000}
	got, _ = cacheablePrefixTokens(readOnly, 2_000, false)
	assert.Equal(t, 800, got)

	both := sessionpin.Pin{LastInputTokens: 10_000, LastCachedReadTokens: 4_000, LastCachedWriteTokens: 3_000}
	got, _ = cacheablePrefixTokens(both, 10_000, false)
	assert.Equal(t, 7_000, got)

	trimmed, trimmedKnown := cacheablePrefixTokens(both, 10_000, true)
	assert.Zero(t, trimmed)
	assert.True(t, trimmedKnown, "a client trim is a measured eviction, not missing evidence")
}

// TestRecordTurnUsage_WritesToStore guards the synchronous UpdateUsage write:
// recordTurnUsage must persist Last* fields in-line so the planner has
// prior-turn evidence by the next turn.
func TestRecordTurnUsage_WritesToStore(t *testing.T) {
	store := newStubPinStore()
	svc := NewService(
		nil,
		nil,
		nil,
		false,
		nil,
		store,
		false,
		"anthropic", "claude-haiku-4-5",
		nil,
	)

	var sessionKey [sessionpin.SessionKeyLen]byte
	for i := range sessionKey {
		sessionKey[i] = byte(i + 1)
	}

	res := turnLoopResult{
		Decision:   router.Decision{Provider: "anthropic", Model: "claude-opus-4-7"},
		SessionKey: sessionKey,
		PinRole:    sessionpin.DefaultRole,
	}
	svc.recordTurnUsage(res, res.Decision.Provider, res.Decision.Model, 1200, 80, 200, 900)

	store.mu.Lock()
	defer store.mu.Unlock()
	assert.Equal(t, 1, store.usageHits, "UpdateUsage must run synchronously on the request path")
	assert.Equal(t, 1200, store.lastUsage.InputTokens)
	assert.Equal(t, 900, store.lastUsage.CachedReadTokens)
	assert.Equal(t, 200, store.lastUsage.CachedWriteTokens)
	assert.Equal(t, 80, store.lastUsage.OutputTokens)
	assert.Equal(t, "claude-opus-4-7", store.lastUsage.ServedModel)
	assert.Equal(t, "anthropic", store.lastUsage.ServedProvider)
	assert.False(t, store.lastUsage.EndedAt.IsZero(), "EndedAt must be stamped — the planner uses IsZero() as its no-prior-usage gate")
}

func TestRecordTurnUsage_ForwardsSwitchHistory(t *testing.T) {
	store := newStubPinStore()
	svc := NewService(
		nil,
		nil,
		nil,
		false,
		nil,
		store,
		false,
		"anthropic", "claude-haiku-4-5",
		nil,
	)

	var sessionKey [sessionpin.SessionKeyLen]byte
	for i := range sessionKey {
		sessionKey[i] = byte(i + 1)
	}

	res := turnLoopResult{
		Decision:            router.Decision{Provider: "anthropic", Model: "claude-opus-4-7"},
		SessionKey:          sessionKey,
		PinRole:             sessionpin.DefaultRole,
		PriorServedModel:    "claude-opus-4-7",
		SessionEverSwitched: true,
	}
	svc.recordTurnUsage(res, res.Decision.Provider, res.Decision.Model, 1200, 80, 200, 900)

	store.mu.Lock()
	defer store.mu.Unlock()
	assert.Equal(t, "claude-opus-4-7", store.lastUsage.PriorServedModel)
	assert.True(t, store.lastUsage.SessionEverSwitched,
		"a fresh role row must inherit an existing switch latch when the served model is unchanged")
}

func TestRecordTurnUsage_HMMDecisionWritesHistoryOnly(t *testing.T) {
	store := newStubPinStore()
	svc := NewService(
		nil,
		nil,
		nil,
		false,
		nil,
		store,
		false,
		"anthropic", "claude-haiku-4-5",
		nil,
	)

	var sessionKey [sessionpin.SessionKeyLen]byte
	for i := range sessionKey {
		sessionKey[i] = byte(i + 1)
	}

	res := turnLoopResult{
		InstallationID: uuid.New(),
		Strategy:       router.StrategyHMMBeta,
		Decision: router.Decision{
			Provider: "anthropic",
			Model:    "claude-sonnet-5",
			Reason:   "hmm_policy(label=high)",
			Metadata: &router.RoutingMetadata{
				Strategy: string(router.StrategyHMMBeta),
				RouteID:  "route-1",
			},
		},
		SessionKey: sessionKey,
		PinRole:    sessionpin.DefaultRole,
		PinTier:    "hmm_fresh_unpinned",
		// Simulate a prior HMM/default-route model so the history role can
		// latch has_ever_switched without mutating the active routing role.
		PriorServedModel: "claude-haiku-4-5",
	}
	svc.recordTurnUsage(res, res.Decision.Provider, res.Decision.Model, 1200, 80, 200, 900)

	store.mu.Lock()
	defer store.mu.Unlock()
	require.Len(t, store.upserts, 1)
	assert.Equal(t, hmmHistoryRole(sessionpin.DefaultRole), store.upserts[0].Role)
	assert.Equal(t, "hmm_policy(label=high)", store.upserts[0].Reason)
	assert.Equal(t, providers.ProviderAnthropic, store.upserts[0].Provider)
	assert.Empty(t, store.upserts[0].Model, "HMM history rows must not be routable pins")
	assert.Equal(t, router.StrategyHMMBeta, store.upserts[0].Strategy)
	assert.Equal(t, []string{hmmHistoryRole(sessionpin.DefaultRole)}, store.usageRoles)
	assert.NotContains(t, store.usageRoles, sessionpin.DefaultRole, "HMM turns must not mutate the active routing pin role")
	assert.Equal(t, 1200, store.lastUsage.InputTokens)
	assert.Equal(t, 900, store.lastUsage.CachedReadTokens)
	assert.Equal(t, 200, store.lastUsage.CachedWriteTokens)
	assert.Equal(t, 80, store.lastUsage.OutputTokens)
	assert.Equal(t, "claude-sonnet-5", store.lastUsage.ServedModel)
	assert.Equal(t, "claude-haiku-4-5", store.lastUsage.PriorServedModel)
	assert.Equal(t, router.StrategyHMMBeta, store.lastUsage.Strategy)
}

func TestRecordTurnUsage_HMMModelChangeWritesCurrentUsageOnly(t *testing.T) {
	store := newStubPinStore()
	svc := NewService(
		nil,
		nil,
		nil,
		false,
		nil,
		store,
		false,
		"anthropic", "claude-haiku-4-5",
		nil,
	)

	var sessionKey [sessionpin.SessionKeyLen]byte
	for i := range sessionKey {
		sessionKey[i] = byte(i + 1)
	}

	res := turnLoopResult{
		InstallationID: uuid.New(),
		Decision: router.Decision{
			Provider: providers.ProviderAnthropic,
			Model:    "claude-sonnet-5",
			Reason:   "hmm_policy(label=high)",
			Metadata: &router.RoutingMetadata{
				Strategy: string(router.StrategyHMM),
				RouteID:  "route-1",
			},
		},
		SessionKey:       sessionKey,
		PinRole:          sessionpin.DefaultRole,
		PinTier:          "hmm_fresh_unpinned",
		PriorServedModel: "claude-haiku-4-5",
	}
	svc.recordTurnUsage(res, res.Decision.Provider, res.Decision.Model, 1200, 80, 200, 900)

	store.mu.Lock()
	defer store.mu.Unlock()
	assert.Equal(t, 1, store.usageHits)
	assert.Equal(t, []string{hmmHistoryRole(sessionpin.DefaultRole)}, store.usageRoles)
	assert.Equal(t, 1200, store.lastUsage.InputTokens)
	assert.Equal(t, 80, store.lastUsage.OutputTokens)
	assert.Equal(t, "claude-sonnet-5", store.lastUsage.ServedModel)
	assert.Equal(t, "claude-haiku-4-5", store.lastUsage.PriorServedModel)
}

func TestRecordHMMTurnHistory_ZeroUsageRefreshesTTLButSkipsUsageWriteback(t *testing.T) {
	store := newStubPinStore()
	svc := NewService(
		nil,
		nil,
		nil,
		false,
		nil,
		store,
		false,
		"anthropic", "claude-haiku-4-5",
		nil,
	)

	var sessionKey [sessionpin.SessionKeyLen]byte
	for i := range sessionKey {
		sessionKey[i] = byte(i + 1)
	}

	res := turnLoopResult{
		InstallationID: uuid.New(),
		Decision: router.Decision{
			Provider: providers.ProviderAnthropic,
			Model:    "claude-sonnet-5",
			Reason:   "hmm_policy(label=high)",
			Metadata: &router.RoutingMetadata{
				Strategy: string(router.StrategyHMM),
				RouteID:  "route-1",
			},
		},
		SessionKey: sessionKey,
		PinRole:    sessionpin.DefaultRole,
	}
	// A failed/empty upstream turn: all usage counts zero.
	svc.recordTurnUsage(res, res.Decision.Provider, res.Decision.Model, 0, 0, 0, 0)

	store.mu.Lock()
	defer store.mu.Unlock()
	require.Len(t, store.upserts, 1, "TTL-refreshing upsert must still run on a zero-usage turn")
	assert.Equal(t, hmmHistoryRole(sessionpin.DefaultRole), store.upserts[0].Role)
	assert.Empty(t, store.usageRoles, "zero-usage turn must not clobber the history row's usage columns")
	assert.Equal(t, 0, store.usageHits)
}

func TestRecordHMMTurnHistory_ZeroUsagePreservesPriorProvider(t *testing.T) {
	store := newStubPinStore()
	store.getFound = true
	store.getPin = sessionpin.Pin{Provider: providers.ProviderMakora}
	svc := NewService(
		nil,
		map[string]providers.Client{providers.ProviderMakora: nil, providers.ProviderOpenAI: nil},
		nil,
		false,
		nil,
		store,
		false,
		providers.ProviderAnthropic, "claude-haiku-4-5",
		nil,
	)

	var sessionKey [sessionpin.SessionKeyLen]byte
	for i := range sessionKey {
		sessionKey[i] = byte(i + 1)
	}

	res := turnLoopResult{
		InstallationID: uuid.New(),
		Decision: router.Decision{
			Provider: providers.ProviderOpenAI,
			Model:    "gpt-5.5",
			Reason:   "hmm_policy(label=high)",
			Metadata: &router.RoutingMetadata{Strategy: string(router.StrategyHMM), RouteID: "route-1"},
		},
		SessionKey: sessionKey,
		PinRole:    sessionpin.DefaultRole,
	}

	svc.recordTurnUsage(res, res.Decision.Provider, res.Decision.Model, 0, 0, 0, 0)

	store.mu.Lock()
	defer store.mu.Unlock()
	require.Len(t, store.upserts, 1)
	assert.Equal(t, providers.ProviderMakora, store.upserts[0].Provider,
		"a failed turn must not replace the provider paired with the last successful HMM model")
}

func TestNormalizeHMMStayPin_RepairsMismatchedProvider(t *testing.T) {
	svc := NewService(
		nil,
		map[string]providers.Client{providers.ProviderMakora: nil, providers.ProviderOpenAI: nil},
		nil,
		false,
		nil,
		nil,
		false,
		providers.ProviderAnthropic, "claude-haiku-4-5",
		nil,
	)
	pin := sessionpin.Pin{
		Provider:        providers.ProviderOpenAI,
		LastServedModel: "deepseek/deepseek-v4-flash",
		LastTurnEndedAt: time.Now(),
		PinnedUntil:     time.Now().Add(time.Hour),
	}

	normalized, ok := svc.normalizeHMMStayPin(router.Request{}, pin)
	require.True(t, ok)
	assert.Equal(t, "deepseek/deepseek-v4-flash", normalized.Model)
	assert.Equal(t, providers.ProviderMakora, normalized.Provider,
		"a sticky HMM pin must resolve the provider from its retained model")
}

func TestNormalizeHMMStayPin_ReResolvesDisabledProvider(t *testing.T) {
	svc := NewService(
		nil,
		map[string]providers.Client{providers.ProviderMakora: nil, providers.ProviderOpenAI: nil},
		nil,
		false,
		nil,
		nil,
		false,
		providers.ProviderAnthropic, "claude-haiku-4-5",
		nil,
	)
	pin := sessionpin.Pin{
		Provider:        providers.ProviderOpenAI,
		LastServedModel: "deepseek/deepseek-v4-flash",
		LastTurnEndedAt: time.Now(),
		PinnedUntil:     time.Now().Add(time.Hour),
	}

	normalized, ok := svc.normalizeHMMStayPin(router.Request{
		EnabledProviders: map[string]struct{}{providers.ProviderMakora: {}},
	}, pin)
	require.True(t, ok)
	assert.Equal(t, providers.ProviderMakora, normalized.Provider,
		"a sticky HMM pin must re-resolve a disabled provider for its retained model")
}

func TestNormalizeHMMStayPin_PreservesCatalogRoutableTerra(t *testing.T) {
	availableProviders := map[string]struct{}{providers.ProviderOpenAI: {}}
	svc := NewService(
		nil,
		map[string]providers.Client{providers.ProviderOpenAI: nil},
		nil,
		false,
		nil,
		nil,
		false,
		providers.ProviderAnthropic, "claude-haiku-4-5",
		nil,
	).WithAvailableModels(catalog.RoutingTargetSet(availableProviders))
	pin := sessionpin.Pin{
		Provider:        providers.ProviderOpenAI,
		Model:           "gpt-5.6-terra",
		LastServedModel: "gpt-5.6-terra",
		LastTurnEndedAt: time.Now(),
		PinnedUntil:     time.Now().Add(time.Hour),
	}

	normalized, ok := svc.normalizeHMMStayPin(router.Request{}, pin)

	require.True(t, ok)
	assert.Equal(t, "gpt-5.6-terra", normalized.Model)
	assert.Equal(t, providers.ProviderOpenAI, normalized.Provider)
}

func TestRecordTurnUsage_HMMEVStayWritesHistoryOnly(t *testing.T) {
	store := newStubPinStore()
	svc := NewService(
		nil,
		nil,
		nil,
		false,
		nil,
		store,
		false,
		"anthropic", "claude-haiku-4-5",
		nil,
	)

	var sessionKey [sessionpin.SessionKeyLen]byte
	for i := range sessionKey {
		sessionKey[i] = byte(i + 1)
	}

	res := turnLoopResult{
		InstallationID: uuid.New(),
		Decision: router.Decision{
			Provider: providers.ProviderAnthropic,
			Model:    "claude-sonnet-5",
			Reason:   hmmHistoryReason,
		},
		Fresh: router.Decision{
			Provider: providers.ProviderMakora,
			Model:    "deepseek/deepseek-v4-flash",
			Reason:   "hmm_policy(classifier 'fast')",
			Metadata: &router.RoutingMetadata{
				Strategy: string(router.StrategyHMM),
				RouteID:  "route-1",
			},
		},
		SessionKey: sessionKey,
		PinRole:    sessionpin.DefaultRole,
		PinTier:    "hmm_ev_stay_ev_negative",
	}
	svc.recordTurnUsage(res, res.Decision.Provider, res.Decision.Model, 1200, 80, 200, 900)

	store.mu.Lock()
	defer store.mu.Unlock()
	require.Len(t, store.upserts, 1)
	assert.Equal(t, hmmHistoryRole(sessionpin.DefaultRole), store.upserts[0].Role)
	assert.Empty(t, store.upserts[0].Model, "HMM history rows must not be routable pins")
	assert.Equal(t, []string{hmmHistoryRole(sessionpin.DefaultRole)}, store.usageRoles)
	assert.Equal(t, 80, store.lastUsage.OutputTokens)
	assert.Equal(t, "claude-sonnet-5", store.lastUsage.ServedModel)
}

func TestStickyStateRole_HMMEVStayTargetsHistory(t *testing.T) {
	res := turnLoopResult{
		StickyHit:  true,
		PinRole:    sessionpin.DefaultRole,
		StickyRole: hmmHistoryRole(sessionpin.DefaultRole),
	}
	assert.Equal(t, hmmHistoryRole(sessionpin.DefaultRole), stickyStateRole(res))
}

func TestStickyStateRole_DefaultsToActivePinRole(t *testing.T) {
	res := turnLoopResult{
		StickyHit: true,
		PinRole:   sessionpin.DefaultRole,
	}
	assert.Equal(t, sessionpin.DefaultRole, stickyStateRole(res))
}

func TestHMMCostGate_StaysOnWarmCacheWhenCheaperFreshDoesNotClearEV(t *testing.T) {
	svc := NewService(
		nil,
		map[string]providers.Client{providers.ProviderAnthropic: nil, providers.ProviderMakora: nil},
		nil,
		false,
		nil,
		nil,
		false,
		"anthropic", "claude-haiku-4-5",
		nil,
	)
	history := sessionpin.Pin{
		Provider:        providers.ProviderAnthropic,
		LastServedModel: "claude-sonnet-5",
		LastTurnEndedAt: time.Now().Add(-30 * time.Second),
	}
	fresh := router.Decision{
		Provider: providers.ProviderMakora,
		Model:    "deepseek/deepseek-v4-flash",
		Reason:   "hmm_policy(classifier 'fast')",
		Metadata: &router.RoutingMetadata{
			Strategy:    string(router.StrategyHMM),
			RouteID:     "route-1",
			ChosenScore: 0.70,
		},
	}

	decision, plan, sticky, stayModel := svc.hmmCostGatedDecision(
		router.Request{},
		sessionpin.Pin{},
		history,
		fresh,
		100,
		false,
	)

	assert.True(t, sticky)
	assert.Equal(t, "claude-sonnet-5", decision.Model)
	assert.Equal(t, providers.ProviderAnthropic, decision.Provider)
	assert.Equal(t, "claude-sonnet-5", stayModel)
	assert.Equal(t, planner.OutcomeStay, plan.Outcome)
	assert.Equal(t, planner.ReasonEVNegative, plan.Reason)
}

func TestHMMCostGate_SwitchesCheaperFreshWhenEVPositive(t *testing.T) {
	svc := NewService(
		nil,
		map[string]providers.Client{providers.ProviderAnthropic: nil, providers.ProviderMakora: nil},
		nil,
		false,
		nil,
		nil,
		false,
		"anthropic", "claude-haiku-4-5",
		nil,
	)
	history := sessionpin.Pin{
		Provider:        providers.ProviderAnthropic,
		LastServedModel: "claude-sonnet-5",
		LastTurnEndedAt: time.Now().Add(-30 * time.Second),
	}
	fresh := router.Decision{
		Provider: providers.ProviderMakora,
		Model:    "deepseek/deepseek-v4-flash",
		Reason:   "hmm_policy(classifier 'fast')",
		Metadata: &router.RoutingMetadata{
			Strategy:    string(router.StrategyHMM),
			RouteID:     "route-1",
			ChosenScore: 0.70,
		},
	}

	decision, plan, sticky, stayModel := svc.hmmCostGatedDecision(
		router.Request{},
		sessionpin.Pin{},
		history,
		fresh,
		10_000,
		false,
	)

	assert.False(t, sticky)
	assert.Equal(t, "deepseek/deepseek-v4-flash", decision.Model)
	assert.Equal(t, "claude-sonnet-5", stayModel)
	assert.Equal(t, planner.OutcomeSwitch, plan.Outcome)
	assert.Equal(t, planner.ReasonEVPositive, plan.Reason)
}

func TestHMMCostGate_SameTierPinSuppressesLateralSwitchWhenEnabled(t *testing.T) {
	svc := NewService(
		nil,
		map[string]providers.Client{providers.ProviderOpenAI: nil, providers.ProviderMakora: nil},
		nil,
		false,
		nil,
		nil,
		false,
		"anthropic", "claude-haiku-4-5",
		nil,
	)
	history := sessionpin.Pin{
		Provider:        providers.ProviderOpenAI,
		LastServedModel: "gpt-4.1-mini",
		LastTurnEndedAt: time.Now().Add(-30 * time.Second),
	}
	fresh := router.Decision{
		Provider: providers.ProviderMakora,
		Model:    "deepseek/deepseek-v4-flash",
		Reason:   "hmm_policy(classifier 'fast')",
		Metadata: &router.RoutingMetadata{
			Strategy:    string(router.StrategyHMM),
			RouteID:     "route-1",
			ChosenScore: 0.70,
		},
	}
	// Confirm both models share the same tier before exercising the guard.
	require.Equal(t, catalog.TierFor(history.LastServedModel), catalog.TierFor(fresh.Model))
	require.NotEqual(t, catalog.TierUnknown, catalog.TierFor(history.LastServedModel))

	// Flag off (default): unaffected, same as any other EV-positive switch.
	decision, plan, sticky, stayModel := svc.hmmCostGatedDecision(
		router.Request{},
		sessionpin.Pin{},
		history,
		fresh,
		10_000,
		false,
	)
	assert.False(t, sticky)
	assert.Equal(t, "deepseek/deepseek-v4-flash", decision.Model)
	assert.Equal(t, "gpt-4.1-mini", stayModel)
	assert.Equal(t, planner.OutcomeSwitch, plan.Outcome)
	assert.Equal(t, planner.ReasonEVPositive, plan.Reason)

	// Flag on: the lateral same-tier switch is suppressed; the pin sticks.
	svc.WithHMMSameTierPin(true)
	decision, plan, sticky, stayModel = svc.hmmCostGatedDecision(
		router.Request{},
		sessionpin.Pin{},
		history,
		fresh,
		10_000,
		false,
	)
	assert.True(t, sticky)
	assert.Equal(t, "gpt-4.1-mini", decision.Model)
	assert.Equal(t, providers.ProviderOpenAI, decision.Provider)
	assert.Equal(t, "gpt-4.1-mini", stayModel)
	assert.Equal(t, planner.OutcomeStay, plan.Outcome)
	assert.Equal(t, planner.ReasonSameTierPinned, plan.Reason)
}

func TestHMMCostGate_SameTierPinDoesNotBlockCrossTierSwitch(t *testing.T) {
	svc := NewService(
		nil,
		map[string]providers.Client{providers.ProviderAnthropic: nil, providers.ProviderMakora: nil},
		nil,
		false,
		nil,
		nil,
		false,
		"anthropic", "claude-haiku-4-5",
		nil,
	).WithHMMSameTierPin(true)
	history := sessionpin.Pin{
		Provider:        providers.ProviderAnthropic,
		LastServedModel: "claude-sonnet-5",
		LastTurnEndedAt: time.Now().Add(-30 * time.Second),
	}
	fresh := router.Decision{
		Provider: providers.ProviderMakora,
		Model:    "deepseek/deepseek-v4-flash",
		Reason:   "hmm_policy(classifier 'fast')",
		Metadata: &router.RoutingMetadata{
			Strategy:    string(router.StrategyHMM),
			RouteID:     "route-1",
			ChosenScore: 0.70,
		},
	}
	require.NotEqual(t, catalog.TierFor(history.LastServedModel), catalog.TierFor(fresh.Model))

	decision, plan, sticky, stayModel := svc.hmmCostGatedDecision(
		router.Request{},
		sessionpin.Pin{},
		history,
		fresh,
		10_000,
		false,
	)

	assert.False(t, sticky)
	assert.Equal(t, "deepseek/deepseek-v4-flash", decision.Model)
	assert.Equal(t, "claude-sonnet-5", stayModel)
	assert.Equal(t, planner.OutcomeSwitch, plan.Outcome)
	assert.Equal(t, planner.ReasonEVPositive, plan.Reason)
}

func TestHMMCostGate_SameTierPinDoesNotBlockConfidentUpgrade(t *testing.T) {
	svc := NewService(
		nil,
		map[string]providers.Client{providers.ProviderAnthropic: nil, providers.ProviderFireworks: nil},
		nil,
		false,
		nil,
		nil,
		false,
		"anthropic", "claude-haiku-4-5",
		nil,
	).WithHMMSameTierPin(true)
	history := sessionpin.Pin{
		Provider:        providers.ProviderFireworks,
		LastServedModel: "moonshotai/kimi-k2.7",
		LastTurnEndedAt: time.Now().Add(-30 * time.Second),
	}
	fresh := router.Decision{
		Provider: providers.ProviderAnthropic,
		Model:    "claude-sonnet-5",
		Reason:   "hmm_policy(classifier 'high')",
		Metadata: &router.RoutingMetadata{
			Strategy:    string(router.StrategyHMM),
			RouteID:     "route-1",
			ChosenScore: 0.85,
		},
	}
	require.NotEqual(t, catalog.TierFor(history.LastServedModel), catalog.TierFor(fresh.Model))

	decision, plan, sticky, stayModel := svc.hmmCostGatedDecision(
		router.Request{},
		sessionpin.Pin{},
		history,
		fresh,
		10_000,
		false,
	)

	assert.False(t, sticky)
	assert.Equal(t, "claude-sonnet-5", decision.Model)
	assert.Equal(t, "moonshotai/kimi-k2.7", stayModel)
	assert.Equal(t, planner.OutcomeSwitch, plan.Outcome)
	assert.Equal(t, hmmReasonConfidentUpgrade, plan.Reason)
}

func TestHMMCostGate_SameTierPinIgnoresUnknownTierModels(t *testing.T) {
	svc := NewService(
		nil,
		map[string]providers.Client{providers.ProviderAnthropic: nil, providers.ProviderMakora: nil},
		nil,
		false,
		nil,
		nil,
		false,
		"anthropic", "claude-haiku-4-5",
		nil,
	).WithHMMSameTierPin(true)
	history := sessionpin.Pin{
		Provider:        providers.ProviderAnthropic,
		LastServedModel: "claude-opus-4-5", // untiered (TierUnknown) in the catalog fixture
		LastTurnEndedAt: time.Now().Add(-30 * time.Second),
	}
	fresh := router.Decision{
		Provider: providers.ProviderMakora,
		Model:    "deepseek/deepseek-v4-flash",
		Reason:   "hmm_policy(classifier 'fast')",
		Metadata: &router.RoutingMetadata{
			Strategy:    string(router.StrategyHMM),
			RouteID:     "route-1",
			ChosenScore: 0.70,
		},
	}
	require.Equal(t, catalog.TierUnknown, catalog.TierFor(history.LastServedModel))

	decision, plan, sticky, stayModel := svc.hmmCostGatedDecision(
		router.Request{},
		sessionpin.Pin{},
		history,
		fresh,
		10_000,
		false,
	)

	assert.False(t, sticky)
	assert.Equal(t, "deepseek/deepseek-v4-flash", decision.Model)
	assert.Equal(t, "claude-opus-4-5", stayModel)
	assert.Equal(t, planner.OutcomeSwitch, plan.Outcome)
	assert.Equal(t, planner.ReasonEVPositive, plan.Reason)
}

func TestHMMCostGate_PhaseChangeFollowsFreshDecision(t *testing.T) {
	svc := NewService(
		nil,
		map[string]providers.Client{providers.ProviderAnthropic: nil, providers.ProviderMakora: nil},
		nil,
		false,
		nil,
		nil,
		false,
		"anthropic", "claude-haiku-4-5",
		nil,
	)
	activePin := sessionpin.Pin{
		Provider:        providers.ProviderAnthropic,
		Model:           "claude-sonnet-5",
		LastServedModel: "claude-sonnet-5",
		Reason:          "hmm_policy:tool_execution(label=explore)",
		LastTurnEndedAt: time.Now().Add(-30 * time.Second),
		PinnedUntil:     time.Now().Add(time.Hour),
	}
	fresh := router.Decision{
		Provider: providers.ProviderMakora,
		Model:    "deepseek/deepseek-v4-flash",
		Reason:   "hmm_policy(classifier 'balanced')",
		Metadata: &router.RoutingMetadata{
			Strategy:    string(router.StrategyHMM),
			RouteID:     "route-1",
			ChosenScore: 0.70,
		},
	}

	decision, plan, sticky, stayModel := svc.hmmCostGatedDecision(
		router.Request{},
		activePin,
		sessionpin.Pin{},
		fresh,
		100,
		false,
	)

	assert.False(t, sticky)
	assert.Equal(t, "deepseek/deepseek-v4-flash", decision.Model)
	assert.Equal(t, "claude-sonnet-5", stayModel)
	assert.Equal(t, planner.OutcomeSwitch, plan.Outcome)
	assert.Equal(t, hmmReasonPhaseChange, plan.Reason)
}

func TestHMMCostGate_HistoryPhaseChangeFollowsFreshDecision(t *testing.T) {
	svc := NewService(
		nil,
		map[string]providers.Client{providers.ProviderAnthropic: nil, providers.ProviderMakora: nil},
		nil,
		false,
		nil,
		nil,
		false,
		"anthropic", "claude-haiku-4-5",
		nil,
	)
	history := sessionpin.Pin{
		Provider:        providers.ProviderAnthropic,
		LastServedModel: "claude-sonnet-5",
		Reason:          "hmm_policy:tool_execution(label=explore)",
		LastTurnEndedAt: time.Now().Add(-30 * time.Second),
		PinnedUntil:     time.Now().Add(time.Hour),
	}
	fresh := router.Decision{
		Provider: providers.ProviderMakora,
		Model:    "deepseek/deepseek-v4-flash",
		Reason:   "hmm_policy(classifier 'balanced')",
		Metadata: &router.RoutingMetadata{
			Strategy:    string(router.StrategyHMM),
			RouteID:     "route-1",
			ChosenScore: 0.70,
		},
	}

	decision, plan, sticky, stayModel := svc.hmmCostGatedDecision(
		router.Request{},
		sessionpin.Pin{},
		history,
		fresh,
		100,
		false,
	)

	assert.False(t, sticky)
	assert.Equal(t, "deepseek/deepseek-v4-flash", decision.Model)
	assert.Equal(t, "claude-sonnet-5", stayModel)
	assert.Equal(t, planner.OutcomeSwitch, plan.Outcome)
	assert.Equal(t, hmmReasonPhaseChange, plan.Reason)
}

func TestHMMCostGate_ExpensiveUpgradeRequiresHighConfidence(t *testing.T) {
	svc := NewService(
		nil,
		map[string]providers.Client{providers.ProviderAnthropic: nil, providers.ProviderFireworks: nil},
		nil,
		false,
		nil,
		nil,
		false,
		"anthropic", "claude-haiku-4-5",
		nil,
	)
	history := sessionpin.Pin{
		Provider:        providers.ProviderFireworks,
		LastServedModel: "moonshotai/kimi-k2.7",
		LastTurnEndedAt: time.Now().Add(-30 * time.Second),
	}
	fresh := router.Decision{
		Provider: providers.ProviderAnthropic,
		Model:    "claude-sonnet-5",
		Reason:   "hmm_policy(classifier 'high')",
		Metadata: &router.RoutingMetadata{
			Strategy:    string(router.StrategyHMM),
			RouteID:     "route-1",
			ChosenScore: 0.84,
		},
	}

	decision, plan, sticky, stayModel := svc.hmmCostGatedDecision(
		router.Request{},
		sessionpin.Pin{},
		history,
		fresh,
		10_000,
		false,
	)

	assert.True(t, sticky)
	assert.Equal(t, "moonshotai/kimi-k2.7", decision.Model)
	assert.Equal(t, "moonshotai/kimi-k2.7", stayModel)
	assert.Equal(t, planner.OutcomeStay, plan.Outcome)
	assert.Equal(t, hmmReasonUpgradeConfidenceLow, plan.Reason)

	fresh.Metadata.ChosenScore = 0.85
	decision, plan, sticky, stayModel = svc.hmmCostGatedDecision(
		router.Request{},
		sessionpin.Pin{},
		history,
		fresh,
		10_000,
		false,
	)

	assert.False(t, sticky)
	assert.Equal(t, "claude-sonnet-5", decision.Model)
	assert.Equal(t, "moonshotai/kimi-k2.7", stayModel)
	assert.Equal(t, planner.OutcomeSwitch, plan.Outcome)
	assert.Equal(t, hmmReasonConfidentUpgrade, plan.Reason)
}

func TestHMMCostGate_LowConfidenceUpgradeKeepsIndependentPlannerSwitch(t *testing.T) {
	svc := NewService(
		nil,
		map[string]providers.Client{providers.ProviderAnthropic: nil, providers.ProviderFireworks: nil},
		nil,
		false,
		nil,
		nil,
		false,
		"anthropic", "claude-haiku-4-5",
		nil,
	).WithPlanner(planner.EVConfig{
		ThresholdUSD:           DefaultPlannerThresholdUSD,
		ExpectedRemainingTurns: DefaultPlannerExpectedRemainingTurns,
		TierUpgradeEnabled:     false,
		ColdPinFollowFresh:     true,
	})
	history := sessionpin.Pin{
		Provider:        providers.ProviderFireworks,
		LastServedModel: "moonshotai/kimi-k2.7",
		LastTurnEndedAt: time.Now().Add(-30 * time.Second),
	}
	fresh := router.Decision{
		Provider: providers.ProviderAnthropic,
		Model:    "claude-sonnet-5",
		Reason:   "hmm_policy(classifier 'high')",
		Metadata: &router.RoutingMetadata{
			Strategy:    string(router.StrategyHMM),
			RouteID:     "route-1",
			ChosenScore: 0.84,
		},
	}

	decision, plan, sticky, stayModel := svc.hmmCostGatedDecision(
		router.Request{},
		sessionpin.Pin{},
		history,
		fresh,
		10_000,
		true,
	)

	assert.False(t, sticky)
	assert.Equal(t, "claude-sonnet-5", decision.Model)
	assert.Equal(t, "moonshotai/kimi-k2.7", stayModel)
	assert.Equal(t, planner.OutcomeSwitch, plan.Outcome)
	assert.Equal(t, planner.ReasonColdPinFresh, plan.Reason)
}

func TestHMMCostGate_IgnoresExpiredActivePin(t *testing.T) {
	svc := NewService(
		nil,
		map[string]providers.Client{providers.ProviderAnthropic: nil, providers.ProviderMakora: nil},
		nil,
		false,
		nil,
		nil,
		false,
		"anthropic", "claude-haiku-4-5",
		nil,
	)
	expired := sessionpin.Pin{
		Provider:        providers.ProviderAnthropic,
		Model:           "claude-sonnet-5",
		LastServedModel: "claude-sonnet-5",
		LastTurnEndedAt: time.Now().Add(-30 * time.Second),
		PinnedUntil:     time.Now().Add(-time.Minute),
	}
	fresh := router.Decision{
		Provider: providers.ProviderMakora,
		Model:    "deepseek/deepseek-v4-flash",
		Reason:   "hmm_policy(classifier 'fast')",
		Metadata: &router.RoutingMetadata{
			Strategy:    string(router.StrategyHMM),
			RouteID:     "route-1",
			ChosenScore: 0.70,
		},
	}

	decision, plan, sticky, stayModel := svc.hmmCostGatedDecision(
		router.Request{},
		expired,
		sessionpin.Pin{},
		fresh,
		100,
		false,
	)

	assert.False(t, sticky)
	assert.Equal(t, "deepseek/deepseek-v4-flash", decision.Model)
	assert.Empty(t, stayModel)
	assert.Equal(t, planner.OutcomeSwitch, plan.Outcome)
	assert.Equal(t, planner.ReasonNoPin, plan.Reason)
}

func TestHMMCostGate_IgnoresNonHMMActivePin(t *testing.T) {
	svc := NewService(
		nil,
		map[string]providers.Client{providers.ProviderAnthropic: nil, providers.ProviderMakora: nil},
		nil,
		false,
		nil,
		nil,
		false,
		"anthropic", "claude-haiku-4-5",
		nil,
	)
	// A warm cluster/planner pin (non-HMM reason) must NOT steer an HMM turn's
	// EV stay — otherwise HMM turns silently reuse ordinary session pins.
	clusterPin := sessionpin.Pin{
		Provider:        providers.ProviderAnthropic,
		Model:           "claude-sonnet-5",
		LastServedModel: "claude-sonnet-5",
		Reason:          "cluster:v0.2",
		LastTurnEndedAt: time.Now().Add(-30 * time.Second),
		PinnedUntil:     time.Now().Add(time.Hour),
	}
	fresh := router.Decision{
		Provider: providers.ProviderMakora,
		Model:    "deepseek/deepseek-v4-flash",
		Reason:   "hmm_policy(classifier 'fast')",
		Metadata: &router.RoutingMetadata{
			Strategy:    string(router.StrategyHMM),
			RouteID:     "route-1",
			ChosenScore: 0.70,
		},
	}

	decision, plan, sticky, stayModel := svc.hmmCostGatedDecision(
		router.Request{},
		clusterPin,
		sessionpin.Pin{},
		fresh,
		100,
		false,
	)

	assert.False(t, sticky, "a non-HMM cluster pin must not win an HMM EV stay")
	assert.Equal(t, "deepseek/deepseek-v4-flash", decision.Model)
	assert.Empty(t, stayModel)
	assert.Equal(t, planner.OutcomeSwitch, plan.Outcome)
	assert.Equal(t, planner.ReasonNoPin, plan.Reason)
}

func TestHMMCostGate_HonorsHMMReasonedActivePin(t *testing.T) {
	svc := NewService(
		nil,
		map[string]providers.Client{providers.ProviderAnthropic: nil, providers.ProviderMakora: nil},
		nil,
		false,
		nil,
		nil,
		false,
		"anthropic", "claude-haiku-4-5",
		nil,
	)
	// The active pin IS HMM-reasoned, so it remains a valid stay candidate: a
	// cheaper fresh pick that doesn't clear EV must stay on it.
	hmmPin := sessionpin.Pin{
		Provider:        providers.ProviderAnthropic,
		Model:           "claude-sonnet-5",
		LastServedModel: "claude-sonnet-5",
		Reason:          "hmm_policy(label=high)",
		LastTurnEndedAt: time.Now().Add(-30 * time.Second),
		PinnedUntil:     time.Now().Add(time.Hour),
	}
	fresh := router.Decision{
		Provider: providers.ProviderMakora,
		Model:    "deepseek/deepseek-v4-flash",
		Reason:   "hmm_policy(classifier 'fast')",
		Metadata: &router.RoutingMetadata{
			Strategy:    string(router.StrategyHMM),
			RouteID:     "route-1",
			ChosenScore: 0.70,
		},
	}

	decision, plan, sticky, stayModel := svc.hmmCostGatedDecision(
		router.Request{},
		hmmPin,
		sessionpin.Pin{},
		fresh,
		100,
		false,
	)

	assert.True(t, sticky, "an HMM-reasoned active pin remains a valid stay candidate")
	assert.Equal(t, "claude-sonnet-5", decision.Model)
	assert.Equal(t, "claude-sonnet-5", stayModel)
	assert.Equal(t, planner.OutcomeStay, plan.Outcome)
	assert.Equal(t, planner.ReasonEVNegative, plan.Reason)
}

func TestHMMCostGate_IgnoresMaxedHistory(t *testing.T) {
	svc := NewService(
		nil,
		map[string]providers.Client{providers.ProviderAnthropic: nil, providers.ProviderMakora: nil},
		nil,
		false,
		nil,
		nil,
		false,
		"anthropic", "claude-haiku-4-5",
		nil,
	)
	history := sessionpin.Pin{
		Provider:         providers.ProviderAnthropic,
		LastServedModel:  "claude-sonnet-5",
		LastOutputTokens: prevTurnMaxedOutThreshold,
		LastTurnEndedAt:  time.Now().Add(-30 * time.Second),
		PinnedUntil:      time.Now().Add(time.Hour),
	}
	fresh := router.Decision{
		Provider: providers.ProviderMakora,
		Model:    "deepseek/deepseek-v4-flash",
		Reason:   "hmm_policy(classifier 'fast')",
		Metadata: &router.RoutingMetadata{
			Strategy:    string(router.StrategyHMM),
			RouteID:     "route-1",
			ChosenScore: 0.70,
		},
	}

	decision, plan, sticky, stayModel := svc.hmmCostGatedDecision(
		router.Request{},
		sessionpin.Pin{},
		history,
		fresh,
		100,
		false,
	)

	assert.False(t, sticky)
	assert.Equal(t, "deepseek/deepseek-v4-flash", decision.Model)
	assert.Empty(t, stayModel)
	assert.Equal(t, planner.OutcomeSwitch, plan.Outcome)
	assert.Equal(t, planner.ReasonNoPin, plan.Reason)
}

// TestLoadPin_DoesNotServeExpiredPostgresPinButKeepsEmitHistory: expired rows
// are routing misses, but has_ever_switched/last_served_model must survive so
// Anthropic emit still strips poisoned thinking blocks.
func TestLoadPin_DoesNotServeExpiredPostgresPinButKeepsEmitHistory(t *testing.T) {
	store := newStubPinStore()
	svc := NewService(
		nil,
		nil,
		nil,
		false,
		nil,
		store,
		false,
		"anthropic", "claude-haiku-4-5",
		nil,
	)
	require.NotNil(t, svc.pinStore)

	var sessionKey [sessionpin.SessionKeyLen]byte
	for i := range sessionKey {
		sessionKey[i] = byte(i + 1)
	}

	store.getPin = sessionpin.Pin{
		SessionKey:      sessionKey,
		Role:            sessionpin.DefaultRole,
		Provider:        "anthropic",
		Model:           "claude-opus-4-7",
		Reason:          "fresh",
		TurnCount:       1,
		PinnedUntil:     time.Now().Add(-time.Minute),
		LastServedModel: "claude-opus-4-7",
		HasEverSwitched: true,
	}
	store.getFound = true

	pin, found := svc.loadPin(context.Background(), sessionKey, sessionpin.DefaultRole)
	assert.False(t, found, "expired Postgres row must not be served")
	assert.Equal(t, "claude-opus-4-7", pin.LastServedModel, "expired row history must be available for emit")
	assert.True(t, pin.HasEverSwitched, "expired row latch must be available for emit")

	res := turnLoopResult{
		Decision:            router.Decision{Model: "claude-opus-4-7"},
		PriorServedModel:    pin.LastServedModel,
		SessionEverSwitched: pin.HasEverSwitched,
	}
	assert.True(t, res.modelSwitched(), "expired switched-session history must still strip thinking blocks")
}

// TestLoadPin_ServesFreshPostgresPin is the companion: a non-expired Postgres
// row is returned verbatim.
func TestLoadPin_ServesFreshPostgresPin(t *testing.T) {
	store := newStubPinStore()
	svc := NewService(
		nil,
		nil,
		nil,
		false,
		nil,
		store,
		false,
		"anthropic", "claude-haiku-4-5",
		nil,
	)

	var sessionKey [sessionpin.SessionKeyLen]byte
	for i := range sessionKey {
		sessionKey[i] = byte(i + 1)
	}

	store.getPin = sessionpin.Pin{
		SessionKey:  sessionKey,
		Role:        sessionpin.DefaultRole,
		Provider:    "anthropic",
		Model:       "claude-opus-4-7",
		Reason:      "fresh",
		TurnCount:   1,
		PinnedUntil: time.Now().Add(time.Hour),
	}
	store.getFound = true

	pin, found := svc.loadPin(context.Background(), sessionKey, sessionpin.DefaultRole)
	require.True(t, found, "non-expired Postgres row must be returned")
	assert.Equal(t, "claude-opus-4-7", pin.Model)
	assert.Equal(t, "anthropic", pin.Provider)
}

func TestLoadPin_RequiresBetaStrategyMatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		request router.Strategy
		stored  router.Strategy
		found   bool
	}{
		{name: "beta exact", request: router.StrategyHMMBeta, stored: router.StrategyHMMBeta, found: true},
		{name: "beta rejects stable", request: router.StrategyHMMBeta, stored: router.StrategyCluster, found: false},
		{name: "beta rejects legacy", request: router.StrategyHMMBeta, stored: "", found: false},
		{name: "stable rejects beta", request: router.StrategyCluster, stored: router.StrategyHMMBeta, found: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newStubPinStore()
			store.getFound = true
			store.getPin = sessionpin.Pin{
				Provider:    providers.ProviderAnthropic,
				Model:       "claude-opus-4-7",
				Strategy:    tt.stored,
				PinnedUntil: time.Now().Add(time.Hour),
			}
			svc := NewService(nil, nil, nil, false, nil, store, false,
				providers.ProviderAnthropic, "claude-haiku-4-5", nil)
			ctx := router.WithStrategy(context.Background(), tt.request)

			pin, found := svc.loadPin(ctx, [sessionpin.SessionKeyLen]byte{1}, sessionpin.DefaultRole)
			assert.Equal(t, tt.found, found)
			if !tt.found {
				assert.Equal(t, sessionpin.Pin{}, pin, "a mismatched pin must not leak reuse or history evidence")
			}
		})
	}
}

func TestSwitchHistoryFromPins_UsesHMMHistory(t *testing.T) {
	now := time.Now()
	active := sessionpin.Pin{
		LastServedModel: "claude-haiku-4-5",
		LastTurnEndedAt: now.Add(-time.Minute),
	}
	hmmHistory := sessionpin.Pin{
		LastServedModel: "claude-sonnet-5",
		LastTurnEndedAt: now,
	}

	prior, everSwitched := switchHistoryFromPins(active, hmmHistory)

	assert.Equal(t, "claude-sonnet-5", prior)
	assert.True(t, everSwitched, "different active/history models must preserve thinking-block stripping")
}

// TestModelSwitched covers switch → stay → stay: once a session has served
// two models, every later same-model turn must keep stripping stale-signed
// thinking blocks or Anthropic 400s — the has_ever_switched latch.
func TestModelSwitched(t *testing.T) {
	tests := []struct {
		name             string
		priorServedModel string
		decisionModel    string
		everSwitched     bool
		want             bool
	}{
		{
			name:          "first turn of a session never switches",
			decisionModel: "claude-opus-4-7",
			want:          false,
		},
		{
			name:             "steady-state same model, never switched",
			priorServedModel: "claude-opus-4-7",
			decisionModel:    "claude-opus-4-7",
			want:             false,
		},
		{
			name:             "transition turn flips models",
			priorServedModel: "deepseek-v4-pro",
			decisionModel:    "claude-opus-4-7",
			want:             true,
		},
		{
			name:             "switch-back transition turn",
			priorServedModel: "deepseek-v4-pro",
			decisionModel:    "claude-opus-4-7",
			everSwitched:     true,
			want:             true,
		},
		{
			name:             "stay turn after a prior switch still strips",
			priorServedModel: "claude-opus-4-7",
			decisionModel:    "claude-opus-4-7",
			everSwitched:     true,
			want:             true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := turnLoopResult{
				Decision:            router.Decision{Model: tc.decisionModel},
				PriorServedModel:    tc.priorServedModel,
				SessionEverSwitched: tc.everSwitched,
			}
			assert.Equal(t, tc.want, res.modelSwitched())
		})
	}
}

func TestService_NewService_HMMUpgradeConfidenceDefaults(t *testing.T) {
	svc := NewService(
		nil,
		map[string]providers.Client{providers.ProviderAnthropic: nil},
		nil,
		false,
		nil,
		nil,
		false,
		"anthropic", "claude-haiku-4-5",
		nil,
	)
	assert.Equal(t, defaultHMMUpgradeConfidenceThreshold, svc.hmmUpgradeConfidenceThreshold)
}

func TestService_WithHMMUpgradeConfidenceThreshold(t *testing.T) {
	svc := NewService(
		nil,
		map[string]providers.Client{providers.ProviderAnthropic: nil},
		nil,
		false,
		nil,
		nil,
		false,
		"anthropic", "claude-haiku-4-5",
		nil,
	)

	svc.WithHMMUpgradeConfidenceThreshold(0.50)
	assert.Equal(t, 0.50, svc.hmmUpgradeConfidenceThreshold)

	svc.WithHMMUpgradeConfidenceThreshold(-0.10)
	assert.Equal(t, 0.50, svc.hmmUpgradeConfidenceThreshold)

	svc.WithHMMUpgradeConfidenceThreshold(1.50)
	assert.Equal(t, 0.50, svc.hmmUpgradeConfidenceThreshold)

	svc.WithHMMUpgradeConfidenceThreshold(0.0)
	assert.Equal(t, 0.0, svc.hmmUpgradeConfidenceThreshold)
}

func TestHMMCostGate_UpgradeThresholdConfigurable(t *testing.T) {
	svc := NewService(
		nil,
		map[string]providers.Client{providers.ProviderAnthropic: nil, providers.ProviderFireworks: nil},
		nil,
		false,
		nil,
		nil,
		false,
		"anthropic", "claude-haiku-4-5",
		nil,
	)
	svc.WithHMMUpgradeConfidenceThreshold(0.20)

	history := sessionpin.Pin{
		Provider:        providers.ProviderFireworks,
		LastServedModel: "moonshotai/kimi-k2.7",
		LastTurnEndedAt: time.Now().Add(-30 * time.Second),
	}
	fresh := router.Decision{
		Provider: providers.ProviderAnthropic,
		Model:    "claude-sonnet-5",
		Reason:   "hmm_policy(classifier 'high')",
		Metadata: &router.RoutingMetadata{
			Strategy:    string(router.StrategyHMM),
			RouteID:     "route-1",
			ChosenScore: 0.30,
		},
	}

	decision, plan, sticky, stayModel := svc.hmmCostGatedDecision(
		router.Request{},
		sessionpin.Pin{},
		history,
		fresh,
		10_000,
		false,
	)

	assert.False(t, sticky)
	assert.Equal(t, "claude-sonnet-5", decision.Model)
	assert.Equal(t, planner.OutcomeSwitch, plan.Outcome)
	assert.Equal(t, hmmReasonConfidentUpgrade, plan.Reason)
	assert.Equal(t, "moonshotai/kimi-k2.7", stayModel)
}
