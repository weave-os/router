package proxy

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type forceModelMapStore struct {
	mu         sync.Mutex
	pins       map[string]sessionpin.Pin
	usageRoles []string
	usageKeys  [][sessionpin.SessionKeyLen]byte
}

func newForceModelMapStore() *forceModelMapStore {
	return &forceModelMapStore{pins: make(map[string]sessionpin.Pin)}
}

func forceModelMapKey(sessionKey [sessionpin.SessionKeyLen]byte, role string) string {
	return fmt.Sprintf("%x:%s", sessionKey, role)
}

func (s *forceModelMapStore) Get(_ context.Context, sessionKey [sessionpin.SessionKeyLen]byte, role string) (sessionpin.Pin, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pin, found := s.pins[forceModelMapKey(sessionKey, role)]
	return pin, found, nil
}

func (s *forceModelMapStore) Consume(_ context.Context, sessionKey [sessionpin.SessionKeyLen]byte, role string, expected router.Strategy) (sessionpin.Pin, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := forceModelMapKey(sessionKey, role)
	pin, found := s.pins[key]
	if !found || !pin.PinnedUntil.After(time.Now()) || (pin.Strategy != expected && !(pin.Strategy == "" && expected != router.StrategyHMMBeta)) {
		return sessionpin.Pin{}, false, nil
	}
	delete(s.pins, key)
	return pin, true, nil
}

func (s *forceModelMapStore) Upsert(_ context.Context, pin sessionpin.Pin) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := forceModelMapKey(pin.SessionKey, pin.Role)
	existing, found := s.pins[key]
	if found && existing.Strategy == pin.Strategy {
		pin.LastServedModel = existing.LastServedModel
		pin.LastTurnEndedAt = existing.LastTurnEndedAt
		pin.LastInputTokens = existing.LastInputTokens
		pin.LastOutputTokens = existing.LastOutputTokens
		pin.HasEverSwitched = existing.HasEverSwitched
	}
	s.pins[key] = pin
	return nil
}

func (s *forceModelMapStore) UpdateUsage(_ context.Context, sessionKey [sessionpin.SessionKeyLen]byte, role string, usage sessionpin.Usage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := forceModelMapKey(sessionKey, role)
	pin, found := s.pins[key]
	if !found || (pin.Strategy != usage.Strategy && !(pin.Strategy == "" && usage.Strategy != router.StrategyHMMBeta)) {
		return nil
	}
	s.usageKeys = append(s.usageKeys, sessionKey)
	s.usageRoles = append(s.usageRoles, role)
	pin.LastServedModel = usage.ServedModel
	pin.LastTurnEndedAt = usage.EndedAt
	pin.LastInputTokens = usage.InputTokens
	pin.LastOutputTokens = usage.OutputTokens
	pin.HasEverSwitched = usage.SessionEverSwitched ||
		(usage.PriorServedModel != "" && usage.PriorServedModel != usage.ServedModel)
	s.pins[key] = pin
	return nil
}

func (*forceModelMapStore) IncrementUpstreamErrors(context.Context, [sessionpin.SessionKeyLen]byte, string, router.Strategy) (int, error) {
	return 0, nil
}
func (*forceModelMapStore) ResetUpstreamErrors(context.Context, [sessionpin.SessionKeyLen]byte, string, router.Strategy) error {
	return nil
}
func (*forceModelMapStore) IncrementOverloadErrors(context.Context, [sessionpin.SessionKeyLen]byte, string, router.Strategy) (int, error) {
	return 0, nil
}
func (*forceModelMapStore) ResetOverloadErrors(context.Context, [sessionpin.SessionKeyLen]byte, string, router.Strategy) error {
	return nil
}
func (*forceModelMapStore) DisableProvider(context.Context, [sessionpin.SessionKeyLen]byte, string, string, router.Strategy) error {
	return nil
}
func (*forceModelMapStore) SweepExpired(context.Context) error { return nil }

func TestRunTurnLoop_ForceModelSessionPinAppliesAcrossChildThreads(t *testing.T) {
	const (
		apiKeyID      = "api-key"
		clientSession = "client-session"
		forcedModel   = "claude-opus-5"
	)
	installationID := uuid.New()
	ctx := context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{SessionID: clientSession})
	parent, err := translate.ParseAnthropic([]byte(`{
		"model":"claude-opus-4-8",
		"messages":[{"role":"user","content":"parent task"}]
	}`))
	require.NoError(t, err)
	child, err := translate.ParseAnthropic([]byte(`{
		"model":"claude-opus-4-8",
		"messages":[{"role":"user","content":"different child task"}]
	}`))
	require.NoError(t, err)

	parentThreadKey := deriveSessionKeyForRequest(ctx, parent, apiKeyID)
	childThreadKey := deriveSessionKeyForRequest(ctx, child, apiKeyID)
	forceSessionKey := deriveForceModelSessionKeyForRequest(ctx, parent, apiKeyID, parentThreadKey)
	require.NotEqual(t, parentThreadKey, childThreadKey)
	require.Equal(t, forceSessionKey, deriveForceModelSessionKeyForRequest(ctx, child, apiKeyID, childThreadKey))

	store := newForceModelMapStore()
	store.pins[forceModelMapKey(forceSessionKey, forceModelSessionRole)] = sessionpin.Pin{
		SessionKey:     forceSessionKey,
		Role:           forceModelSessionRole,
		InstallationID: installationID,
		Provider:       providers.ProviderAnthropic,
		Model:          forcedModel,
		Reason:         translate.ReasonUserForceModel,
		Strategy:       router.StrategyCluster,
		PinnedUntil:    pinNeverExpires,
	}
	freshRouter := &tierProbeRouter{available: map[string]struct{}{"claude-haiku-4-5": {}}}
	svc := NewService(freshRouter, nil, nil, false, nil, store, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil)
	features := child.RoutingFeatures(false)

	ctx = router.WithStrategy(ctx, router.StrategyHMMBeta)
	result, err := svc.runTurnLoop(ctx, child, features, apiKeyID, installationID, "", nil, router.Request{
		RequestedModel: features.Model,
	})
	require.NoError(t, err)

	assert.Equal(t, forcedModel, result.Decision.Model)
	assert.Equal(t, translate.ReasonUserForceModel, result.Decision.Reason)
	assert.True(t, result.StickyHit)
	assert.False(t, result.HardPinned)
	assert.Equal(t, childThreadKey, result.SessionKey, "routing state must remain scoped to the child thread")
	assert.Equal(t, roleForTier(result.RequestedTier), result.PinRole)
	assert.Empty(t, freshRouter.captured, "session force must bypass the scorer")

	store.mu.Lock()
	defer store.mu.Unlock()
	_, controlStillPresent := store.pins[forceModelMapKey(forceSessionKey, forceModelSessionRole)]
	_, historyPresent := store.pins[forceModelMapKey(childThreadKey, forceModelHistoryRole(result.PinRole))]
	assert.True(t, controlStillPresent)
	assert.True(t, historyPresent)
	assert.Equal(t, pinNeverExpires, store.pins[forceModelMapKey(childThreadKey, forceModelHistoryRole(result.PinRole))].PinnedUntil)
}

func TestRunTurnLoop_DroppedSessionForcePreservesThreadPin(t *testing.T) {
	const apiKeyID = "api-key"
	installationID := uuid.New()
	ctx := context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{SessionID: "client-session"})
	env, err := translate.ParseAnthropic([]byte(`{
		"model":"claude-opus-4-8",
		"messages":[{"role":"user","content":"task"}]
	}`))
	require.NoError(t, err)
	features := env.RoutingFeatures(false)
	threadKey := deriveSessionKeyForRequest(ctx, env, apiKeyID)
	forceKey := deriveForceModelSessionKeyForRequest(ctx, env, apiKeyID, threadKey)
	role := roleForTier(catalog.TierFor(features.Model))
	store := newForceModelMapStore()
	store.pins[forceModelMapKey(forceKey, forceModelSessionRole)] = sessionpin.Pin{
		SessionKey:     forceKey,
		Role:           forceModelSessionRole,
		InstallationID: installationID,
		Provider:       providers.ProviderAnthropic,
		Model:          "claude-opus-5",
		Reason:         translate.ReasonUserForceModel,
		PinnedUntil:    pinNeverExpires,
	}
	store.pins[forceModelMapKey(threadKey, role)] = sessionpin.Pin{
		SessionKey:     threadKey,
		Role:           role,
		InstallationID: installationID,
		Provider:       providers.ProviderOpenAI,
		Model:          "gpt-5.5",
		Reason:         "cluster:existing",
		Strategy:       router.StrategyCluster,
		PinnedUntil:    time.Now().Add(time.Hour),
	}
	freshRouter := &tierProbeRouter{available: map[string]struct{}{"claude-haiku-4-5": {}}}
	svc := NewService(freshRouter, nil, nil, false, nil, store, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil)
	ctx = router.WithStrategy(ctx, router.StrategyCluster)

	result, err := svc.runTurnLoop(ctx, env, features, apiKeyID, installationID, "", nil, router.Request{
		RequestedModel: features.Model,
		EnabledProviders: map[string]struct{}{
			providers.ProviderOpenAI: {},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "gpt-5.5", result.Decision.Model)
	assert.True(t, result.StickyHit)
	assert.True(t, result.ForcedPinDropped)
	assert.Equal(t, "provider_not_enabled", result.ForcedPinDropReason)
}

func TestRunTurnLoop_ClearTombstoneBlocksLegacyForce(t *testing.T) {
	const apiKeyID = "api-key"
	installationID := uuid.New()
	ctx := context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{SessionID: "client-session"})
	ctx = router.WithStrategy(ctx, router.StrategyCluster)
	env, err := translate.ParseAnthropic([]byte(`{
		"model":"claude-opus-4-8",
		"messages":[{"role":"user","content":"task after unforce"}]
	}`))
	require.NoError(t, err)
	features := env.RoutingFeatures(false)
	threadKey := deriveSessionKeyForRequest(ctx, env, apiKeyID)
	forceKey := deriveForceModelSessionKeyForRequest(ctx, env, apiKeyID, threadKey)
	role := roleForTier(catalog.TierFor(features.Model))
	store := newForceModelMapStore()
	store.pins[forceModelMapKey(forceKey, forceModelSessionRole)] = sessionpin.Pin{
		SessionKey:     forceKey,
		Role:           forceModelSessionRole,
		InstallationID: installationID,
		Reason:         userUnforcedReason,
		PinnedUntil:    pinNeverExpires,
	}
	store.pins[forceModelMapKey(threadKey, forceModelHistoryRole(role))] = sessionpin.Pin{
		SessionKey:      threadKey,
		Role:            forceModelHistoryRole(role),
		LastServedModel: "claude-opus-5",
		LastTurnEndedAt: time.Now(),
		PinnedUntil:     pinNeverExpires,
	}
	freshRouter := &tierProbeRouter{
		available: map[string]struct{}{"claude-haiku-4-5": {}},
	}
	svc := NewService(freshRouter, nil, nil, false, nil, store, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil)

	result, err := svc.runTurnLoop(ctx, env, features, apiKeyID, installationID, "", nil, router.Request{
		RequestedModel: features.Model,
	})
	require.NoError(t, err)
	assert.Equal(t, "claude-haiku-4-5", result.Decision.Model)
	assert.NotEqual(t, translate.ReasonUserForceModel, result.Decision.Reason)
	assert.Equal(t, "claude-opus-5", result.PriorServedModel)
	assert.NotEmpty(t, freshRouter.captured)
}

func TestRunTurnLoop_SessionForceOverridesAuxiliaryHardPin(t *testing.T) {
	const apiKeyID = "api-key"
	installationID := uuid.New()
	ctx := context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{SessionID: "client-session"})
	env, err := translate.ParseAnthropic([]byte(`{
		"model":"claude-opus-4-8",
		"system":"Your task is to create a detailed summary of the conversation so far.",
		"messages":[{"role":"user","content":"summarize"}]
	}`))
	require.NoError(t, err)
	threadKey := deriveSessionKeyForRequest(ctx, env, apiKeyID)
	forceKey := deriveForceModelSessionKeyForRequest(ctx, env, apiKeyID, threadKey)
	store := newForceModelMapStore()
	store.pins[forceModelMapKey(forceKey, forceModelSessionRole)] = sessionpin.Pin{
		SessionKey:     forceKey,
		Role:           forceModelSessionRole,
		InstallationID: installationID,
		Provider:       providers.ProviderAnthropic,
		Model:          "claude-opus-5",
		Reason:         translate.ReasonUserForceModel,
		PinnedUntil:    pinNeverExpires,
	}
	freshRouter := &tierProbeRouter{available: map[string]struct{}{"claude-haiku-4-5": {}}}
	svc := NewService(freshRouter, nil, nil, false, nil, store, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil)
	features := env.RoutingFeatures(false)

	result, err := svc.runTurnLoop(ctx, env, features, apiKeyID, installationID, "", nil, router.Request{
		RequestedModel: features.Model,
	})
	require.NoError(t, err)
	assert.Equal(t, "claude-opus-5", result.Decision.Model)
	assert.Equal(t, translate.ReasonUserForceModel, result.Decision.Reason)
	assert.False(t, result.HardPinned)
	assert.Equal(t, threadKey, result.SessionKey)
	assert.Empty(t, freshRouter.captured)
}

func TestApplyForceModelCommand_WritesAndClearsSessionControl(t *testing.T) {
	const apiKeyID = "api-key"
	installationID := uuid.New()
	ctx := context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{SessionID: "client-session"})
	env, err := translate.ParseAnthropic([]byte(`{
		"model":"claude-opus-4-8",
		"messages":[{"role":"user","content":"task"}]
	}`))
	require.NoError(t, err)
	threadKey := deriveSessionKeyForRequest(ctx, env, apiKeyID)
	forceKey := deriveForceModelSessionKeyForRequest(ctx, env, apiKeyID, threadKey)
	store := newForceModelMapStore()
	svc := NewService(nil, nil, nil, false, nil, store, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil)

	forcedModel, _, err := svc.applyForceModelCommand(ctx, env, translate.ForceModelResult{Model: "opus"}, installationID, threadKey, forceKey)
	require.NoError(t, err)
	assert.Equal(t, "claude-opus-5", forcedModel)
	pin, found, err := store.Get(ctx, forceKey, forceModelSessionRole)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, translate.ReasonUserForceModel, pin.Reason)
	assert.Equal(t, pinNeverExpires, pin.PinnedUntil)
	for _, role := range forceModelClearRoles() {
		_, threadForceFound, getErr := store.Get(ctx, threadKey, role)
		require.NoError(t, getErr)
		assert.False(t, threadForceFound, "new force state must have only one authoritative row")
	}

	nextModel, _, err := svc.applyForceModelCommand(ctx, env, translate.ForceModelResult{Model: "sonnet"}, installationID, threadKey, forceKey)
	require.NoError(t, err)
	assert.Equal(t, "claude-sonnet-5", nextModel)
	switched, found, err := store.Get(ctx, forceKey, forceModelSessionRole)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "claude-opus-5", switched.LastServedModel)

	_, _, err = svc.applyForceModelCommand(ctx, env, translate.ForceModelResult{Clear: true}, installationID, threadKey, forceKey)
	require.NoError(t, err)
	cleared, found, err := store.Get(ctx, forceKey, forceModelSessionRole)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, pinNeverExpires, cleared.PinnedUntil, "the clear tombstone prevents legacy child pins from reviving")
	assert.Empty(t, cleared.Model)
	assert.Equal(t, "claude-sonnet-5", cleared.LastServedModel)
	assert.True(t, cleared.HasEverSwitched)
	assert.Equal(t, userUnforcedReason, cleared.Reason)
}

func TestRecordTurnUsage_ForcedDecisionWritesThreadHistoryOnly(t *testing.T) {
	store := newForceModelMapStore()
	svc := NewService(nil, nil, nil, false, nil, store, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil)
	threadKey := [sessionpin.SessionKeyLen]byte{1}
	controlKey := [sessionpin.SessionKeyLen]byte{2}
	role := roleForTier(catalog.TierFor("claude-opus-5"))
	store.pins[forceModelMapKey(controlKey, forceModelSessionRole)] = sessionpin.Pin{
		SessionKey: controlKey,
		Role:       forceModelSessionRole,
		Provider:   providers.ProviderAnthropic,
		Model:      "claude-opus-5",
		Reason:     translate.ReasonUserForceModel,
	}
	store.pins[forceModelMapKey(threadKey, forceModelHistoryRole(role))] = sessionpin.Pin{
		SessionKey: threadKey,
		Role:       forceModelHistoryRole(role),
	}

	svc.recordTurnUsage(turnLoopResult{
		SessionKey: threadKey,
		PinRole:    role,
		Decision: router.Decision{
			Provider: providers.ProviderAnthropic,
			Model:    "claude-opus-5",
			Reason:   translate.ReasonUserForceModel,
		},
	}, providers.ProviderAnthropic, "claude-opus-5", 100, 10, 0, 0)

	store.mu.Lock()
	defer store.mu.Unlock()
	require.Equal(t, []string{forceModelHistoryRole(role)}, store.usageRoles)
	require.Equal(t, [][sessionpin.SessionKeyLen]byte{threadKey}, store.usageKeys)
	assert.Empty(t, store.pins[forceModelMapKey(controlKey, forceModelSessionRole)].LastServedModel)
}
