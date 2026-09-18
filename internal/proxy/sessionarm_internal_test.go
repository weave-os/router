package proxy

import (
	"context"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"
)

const (
	armOpus   = "claude-opus-4-7"
	armSonnet = "claude-sonnet-5"
	armAstra  = "gpt-6-astra"
	armLuna   = "gpt-5.6-luna"
)

// keyedPinStore persists rows by (session key, role) so the arm row, the
// thread pin and the HMM history row coexist and survive across turns.
type keyedPinStore struct {
	stubPinStore
	rowsMu sync.Mutex
	rows   map[string]sessionpin.Pin
}

func newKeyedPinStore() *keyedPinStore {
	return &keyedPinStore{rows: map[string]sessionpin.Pin{}}
}

func pinRowKey(key [sessionpin.SessionKeyLen]byte, role string) string {
	return hex.EncodeToString(key[:]) + "/" + role
}

func (s *keyedPinStore) Get(_ context.Context, key [sessionpin.SessionKeyLen]byte, role string) (sessionpin.Pin, bool, error) {
	s.rowsMu.Lock()
	defer s.rowsMu.Unlock()
	pin, ok := s.rows[pinRowKey(key, role)]
	return pin, ok, nil
}

func (s *keyedPinStore) Upsert(_ context.Context, p sessionpin.Pin) error {
	s.rowsMu.Lock()
	defer s.rowsMu.Unlock()
	s.rows[pinRowKey(p.SessionKey, p.Role)] = p
	return nil
}

func (s *keyedPinStore) seed(key [sessionpin.SessionKeyLen]byte, role string, p sessionpin.Pin) {
	p.SessionKey, p.Role = key, role
	if p.PinnedUntil.IsZero() {
		p.PinnedUntil = time.Now().Add(time.Hour)
	}
	s.rowsMu.Lock()
	defer s.rowsMu.Unlock()
	s.rows[pinRowKey(key, role)] = p
}

func (s *keyedPinStore) armRows() []sessionpin.Pin {
	s.rowsMu.Lock()
	defer s.rowsMu.Unlock()
	var out []sessionpin.Pin
	for _, p := range s.rows {
		if p.Role == sessionArmRole {
			out = append(out, p)
		}
	}
	return out
}

func sessionArmCtx(mode flags.SessionArmPinMode, clientSession string) context.Context {
	ctx := context.Background()
	if mode != "" {
		ctx = flags.WithOverrides(ctx, flags.Overrides{Strings: map[flags.Key]string{flags.KeySessionArmPin: string(mode)}})
	}
	if clientSession != "" {
		ctx = context.WithValue(ctx, ClientIdentityContextKey{}, ClientIdentity{SessionID: clientSession})
	}
	return ctx
}

func sessionArmEnvelope(t *testing.T, body string) (*translate.RequestEnvelope, translate.RoutingFeatures) {
	t.Helper()
	env, err := translate.ParseAnthropic([]byte(body))
	require.NoError(t, err)
	return env, env.RoutingFeatures(false)
}

func sessionArmMainLoopEnvelope(t *testing.T, prompt string) (*translate.RequestEnvelope, translate.RoutingFeatures) {
	t.Helper()
	return sessionArmEnvelope(t, fmt.Sprintf(`{"model":"claude-opus-4-8","max_tokens":8192,"system":"sys","messages":[{"role":"user","content":%q}]}`, prompt))
}

func sessionArmToolResultEnvelope(t *testing.T, prompt string) (*translate.RequestEnvelope, translate.RoutingFeatures) {
	t.Helper()
	return sessionArmEnvelope(t, fmt.Sprintf(`{"model":"claude-opus-4-8","max_tokens":8192,"system":"sys","messages":[
		{"role":"user","content":%q},
		{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"file body"}]}]}`, prompt))
}

func sessionArmSubAgentEnvelope(t *testing.T, prompt string) (*translate.RequestEnvelope, translate.RoutingFeatures) {
	t.Helper()
	return sessionArmEnvelope(t, fmt.Sprintf(`{"model":"claude-opus-4-8","max_tokens":8192,"system":"sys","metadata":{"user_id":"subagent:abc"},"messages":[{"role":"user","content":%q}]}`, prompt))
}

func sessionArmService(store sessionpin.Store, scorer router.Router) *Service {
	available := map[string]struct{}{providers.ProviderAnthropic: {}, providers.ProviderOpenAI: {}}
	return NewService(scorer, nil, nil, false, nil, store, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil).
		WithAvailableModels(catalog.RoutingTargetSet(available)).
		WithDeploymentKeyedProviders(available)
}

func sessionArmAuthoritativeService(store sessionpin.Store, policyRouter router.Router, strategy router.Strategy) *Service {
	return sessionArmService(store, nil).WithPolicyStrategy(policy.StrategySpec{
		Strategy: strategy,
		Router:   policyRouter,
		Capabilities: policy.Capabilities{
			SchemaVersion:                 policy.SchemaVersionV1,
			AuthoritativePerTurnSelection: true,
		},
	})
}

func runSessionArmTurn(t *testing.T, svc *Service, ctx context.Context, env *translate.RequestEnvelope, feats translate.RoutingFeatures, installation uuid.UUID, mutate func(*router.Request)) turnLoopResult {
	t.Helper()
	req := router.Request{
		RequestedModel:       feats.Model,
		EstimatedInputTokens: feats.Tokens,
		HasTools:             feats.HasTools,
		ConversationMessages: conversationMessagesForRouting(env),
	}
	if mutate != nil {
		mutate(&req)
	}
	res, err := svc.runTurnLoop(ctx, env, feats, "api-key", installation, "", http.Header{}, req)
	require.NoError(t, err)
	return res
}

func armDecision(model string) router.Decision {
	provider := providers.ProviderAnthropic
	if strings.HasPrefix(model, "gpt-") {
		provider = providers.ProviderOpenAI
	}
	return router.Decision{Provider: provider, Model: model, Reason: "cluster:v0.2"}
}

func armKeyFor(ctx context.Context, env *translate.RequestEnvelope) [sessionpin.SessionKeyLen]byte {
	thread := deriveSessionKey(env, "api-key", clientSessionIDForRequest(ctx, env))
	return deriveConversationSessionKeyForRequest(ctx, env, "api-key", thread, requestcontext.SessionArmConversationKey)
}

func armPin(model string) sessionpin.Pin {
	d := armDecision(model)
	return sessionpin.Pin{Provider: d.Provider, Model: model, Reason: ReasonSessionArmPin}
}

// --- anchoring -------------------------------------------------------------

func TestSessionArm_FirstMainLoopTurnAnchorsArm(t *testing.T) {
	store := newKeyedPinStore()
	scorer := &authoritativeTestRouter{decision: router.Decision{
		Provider: providers.ProviderAnthropic, Model: armOpus, Reason: "cluster:v0.2",
		Metadata: &router.RoutingMetadata{
			RescueModels:       []string{armOpus, armAstra, armLuna},
			CandidateProviders: map[string]string{armAstra: providers.ProviderOpenAI},
		},
	}}
	svc := sessionArmService(store, scorer)
	ctx := sessionArmCtx(flags.SessionArmPinMain, "sess-1")
	env, feats := sessionArmMainLoopEnvelope(t, "first prompt")

	res := runSessionArmTurn(t, svc, ctx, env, feats, uuid.New(), nil)

	assert.Equal(t, armOpus, res.Decision.Model)
	assert.Equal(t, "cluster:v0.2", res.Decision.Reason, "the anchoring turn keeps the policy's own reason")
	assert.True(t, res.SessionArmAnchored)
	assert.False(t, res.SessionArmHeld)
	assert.Equal(t, flags.SessionArmPinMain, res.SessionArmMode)
	assert.Equal(t, armOpus, res.SessionArmModel)
	rows := store.armRows()
	require.Len(t, rows, 1)
	assert.Equal(t, armOpus, rows[0].Model)
	assert.Equal(t, ReasonSessionArmPin, rows[0].Reason)
	assert.Equal(t, armAstra, rows[0].PairedModel, "the policy's first stand-in is kept as the arm's pair")
	assert.Equal(t, providers.ProviderOpenAI, rows[0].PairedProvider)
	assert.Equal(t, armKeyFor(ctx, env), rows[0].SessionKey)
}

func TestSessionArm_ForcedFirstTurnDoesNotAnchor(t *testing.T) {
	store := newKeyedPinStore()
	scorer := &authoritativeTestRouter{decision: armDecision(armSonnet)}
	svc := sessionArmService(store, scorer)
	ctx := sessionArmCtx(flags.SessionArmPinMain, "sess-1")
	env, feats := sessionArmMainLoopEnvelope(t, "first prompt")
	forced := armPin(armOpus)
	forced.Reason = translate.ReasonUserForceModel
	store.seed(deriveSessionKey(env, "api-key", "sess-1"), roleForTier(catalog.TierFor(feats.Model)), forced)

	res := runSessionArmTurn(t, svc, ctx, env, feats, uuid.New(), nil)

	assert.Equal(t, armOpus, res.Decision.Model)
	assert.Empty(t, scorer.requests)
	assert.False(t, res.SessionArmAnchored)
	assert.Empty(t, store.armRows(), "a user force is not an arm the router assigned")
}

func TestSessionArm_UtilityTurnsAreNotCovered(t *testing.T) {
	store := newKeyedPinStore()
	scorer := &authoritativeTestRouter{decision: armDecision(armSonnet)}
	svc := sessionArmService(store, scorer)
	ctx := sessionArmCtx(flags.SessionArmPinAll, "sess-1")
	mainEnv, _ := sessionArmMainLoopEnvelope(t, "first prompt")
	store.seed(armKeyFor(ctx, mainEnv), sessionArmRole, armPin(armOpus))
	env, feats := sessionArmEnvelope(t, `{"model":"claude-opus-4-8","max_tokens":8192,"system":"sys","messages":[{"role":"user","content":"Your task is to create a detailed summary of the conversation so far. Do not call any tools."}]}`)
	require.Equal(t, turntype.Compaction, turntype.Detect(env, feats, "", ""))

	res := runSessionArmTurn(t, svc, ctx, env, feats, uuid.New(), nil)

	assert.True(t, res.HardPinned)
	assert.NotEqual(t, armOpus, res.Decision.Model)
	assert.Empty(t, res.SessionArmMode)
	assert.Len(t, store.armRows(), 1)
	assert.Equal(t, armOpus, store.armRows()[0].Model)
	assert.Nil(t, sessionArmLogFields(res))
}

// --- holding ---------------------------------------------------------------

func TestSessionArm_HeldOverAuthoritativePolicyPick(t *testing.T) {
	store := newKeyedPinStore()
	strategy := router.Strategy("hmm-test")
	policyRouter := &authoritativeTestRouter{decision: router.Decision{
		Provider: providers.ProviderOpenAI, Model: armLuna, Reason: "hmm-test_policy",
	}}
	svc := sessionArmAuthoritativeService(store, policyRouter, strategy)
	ctx := router.WithStrategy(sessionArmCtx(flags.SessionArmPinMain, "sess-1"), strategy)
	env, feats := sessionArmMainLoopEnvelope(t, "first prompt")
	arm := armPin(armOpus)
	arm.Strategy = strategy
	arm.PairedModel, arm.PairedProvider = armAstra, providers.ProviderOpenAI
	store.seed(armKeyFor(ctx, env), sessionArmRole, arm)

	for turn := 0; turn < 3; turn++ {
		res := runSessionArmTurn(t, svc, ctx, env, feats, uuid.New(), nil)
		assert.Equal(t, armOpus, res.Decision.Model, "turn %d", turn)
		assert.Equal(t, ReasonSessionArmPin, res.Decision.Reason)
		assert.True(t, res.SessionArmHeld)
		assert.True(t, res.StickyHit)
		assert.Equal(t, sessionArmPinTier, res.PinTier)
		assert.Empty(t, res.SessionArmOverride)
		assert.Equal(t, armOpus, res.SessionArmModel)
		require.NotNil(t, res.Decision.Metadata)
		_, _, _, ok := svc.policyOutcomeRoute(res, res.Decision)
		assert.False(t, ok, "a held turn reports no policy outcome")
	}
	assert.Empty(t, policyRouter.requests, "a held turn does not consult the policy")
	rows := store.armRows()
	require.Len(t, rows, 1)
	assert.Equal(t, armOpus, rows[0].Model)
}

func TestSessionArm_HeldOnToolResultOverScorer(t *testing.T) {
	store := newKeyedPinStore()
	scorer := &authoritativeTestRouter{decision: armDecision(armSonnet)}
	svc := sessionArmService(store, scorer)
	ctx := sessionArmCtx(flags.SessionArmPinMain, "sess-1")
	env, feats := sessionArmToolResultEnvelope(t, "first prompt")
	store.seed(armKeyFor(ctx, env), sessionArmRole, armPin(armOpus))
	// A thread pin the planner would otherwise STAY on.
	store.seed(deriveSessionKey(env, "api-key", "sess-1"), roleForTier(catalog.TierFor(feats.Model)), sessionpin.Pin{
		Provider: providers.ProviderAnthropic, Model: armSonnet, Reason: "cluster:v0.2",
	})

	res := runSessionArmTurn(t, svc, ctx, env, feats, uuid.New(), nil)

	assert.Equal(t, armOpus, res.Decision.Model)
	assert.True(t, res.SessionArmHeld)
	assert.Empty(t, scorer.requests)
}

func TestSessionArm_HeldDecisionCarriesRescueOrderWithoutArm(t *testing.T) {
	svc := sessionArmService(newKeyedPinStore(), nil)
	arm := armPin(armOpus)
	arm.PairedModel, arm.PairedProvider = armAstra, providers.ProviderOpenAI

	decision := svc.sessionArmDecision(arm)

	require.NotNil(t, decision.Metadata)
	order := decision.Metadata.RescueModels
	require.NotEmpty(t, order)
	assert.Equal(t, armAstra, order[0], "the anchor's pair rescues first")
	assert.NotContains(t, order, armOpus)
	for _, model := range order {
		assert.LessOrEqual(t, catalog.TierFor(model), catalog.TierHigh)
		assert.NotEqual(t, catalog.TierUnknown, catalog.TierFor(model))
	}
	// Same-tier stand-ins outrank lower tiers.
	sawLower := false
	for _, model := range order {
		if catalog.TierFor(model) < catalog.TierHigh {
			sawLower = true
		} else {
			assert.False(t, sawLower, "high-tier %s listed after a lower tier", model)
		}
	}
}

func TestSessionArm_RescueDecisionDoesNotRewriteArm(t *testing.T) {
	store := newKeyedPinStore()
	scorer := &authoritativeTestRouter{decision: armDecision(armSonnet)}
	svc := sessionArmService(store, scorer)
	ctx := sessionArmCtx(flags.SessionArmPinMain, "sess-1")
	env, feats := sessionArmMainLoopEnvelope(t, "first prompt")
	store.seed(armKeyFor(ctx, env), sessionArmRole, armPin(armOpus))

	held := runSessionArmTurn(t, svc, ctx, env, feats, uuid.New(), nil)
	require.True(t, held.SessionArmHeld)
	siblings := svc.siblingFailoverDecisions(ctx, held.Decision, 0, 0, contextWindowOutputReserve)
	require.NotEmpty(t, siblings, "a failing arm turn must have somewhere to go")
	assert.NotEqual(t, armOpus, siblings[0].Model)
	assert.Equal(t, ReasonSiblingFailover, siblings[0].Reason)

	// The next turn returns to the arm: the rescue never touched the arm row.
	next := runSessionArmTurn(t, svc, ctx, env, feats, uuid.New(), nil)
	assert.Equal(t, armOpus, next.Decision.Model)
	assert.True(t, next.SessionArmHeld)
	rows := store.armRows()
	require.Len(t, rows, 1)
	assert.Equal(t, armOpus, rows[0].Model)
}

// --- hard overrides --------------------------------------------------------

func TestSessionArm_UserForceOutranksArm(t *testing.T) {
	store := newKeyedPinStore()
	scorer := &authoritativeTestRouter{decision: armDecision(armSonnet)}
	svc := sessionArmService(store, scorer)
	ctx := sessionArmCtx(flags.SessionArmPinMain, "sess-1")
	env, feats := sessionArmMainLoopEnvelope(t, "first prompt")
	store.seed(armKeyFor(ctx, env), sessionArmRole, armPin(armOpus))
	forced := armPin(armLuna)
	forced.Reason = translate.ReasonUserForceModel
	store.seed(deriveSessionKey(env, "api-key", "sess-1"), roleForTier(catalog.TierFor(feats.Model)), forced)

	res := runSessionArmTurn(t, svc, ctx, env, feats, uuid.New(), nil)

	assert.Equal(t, armLuna, res.Decision.Model)
	assert.False(t, res.SessionArmHeld)
	assert.Equal(t, sessionArmOverrideForceModel, res.SessionArmOverride)
	assert.Equal(t, armOpus, res.SessionArmModel)
	assert.Equal(t, armOpus, store.armRows()[0].Model, "the arm survives the force")
}

func TestSessionArm_RequestForceModelOutranksArm(t *testing.T) {
	store := newKeyedPinStore()
	scorer := &authoritativeTestRouter{decision: armDecision(armSonnet)}
	svc := sessionArmService(store, scorer)
	ctx := sessionArmCtx(flags.SessionArmPinMain, "sess-1")
	env, feats := sessionArmMainLoopEnvelope(t, "first prompt")
	store.seed(armKeyFor(ctx, env), sessionArmRole, armPin(armOpus))

	res := runSessionArmTurn(t, svc, ctx, env, feats, uuid.New(), func(req *router.Request) {
		req.ForceModel = armLuna
	})

	assert.Equal(t, armLuna, res.Decision.Model)
	assert.Equal(t, translate.ReasonUserForceModel, res.Decision.Reason)
	assert.False(t, res.SessionArmHeld)
	assert.Equal(t, sessionArmOverrideForceModel, res.SessionArmOverride)
	assert.Equal(t, armOpus, res.SessionArmModel)
	assert.Equal(t, armOpus, store.armRows()[0].Model)
}

func TestSessionArm_SessionDemotionOverridesForTheTurnThenReturns(t *testing.T) {
	store := newKeyedPinStore()
	scorer := &authoritativeTestRouter{decision: armDecision(armAstra)}
	svc := sessionArmService(store, scorer)
	ctx := sessionArmCtx(flags.SessionArmPinMain, "sess-1")
	env, feats := sessionArmMainLoopEnvelope(t, "first prompt")
	store.seed(armKeyFor(ctx, env), sessionArmRole, armPin(armOpus))
	threadKey := deriveSessionKey(env, "api-key", "sess-1")
	role := roleForTier(catalog.TierFor(feats.Model))
	store.seed(threadKey, role, sessionpin.Pin{
		Provider: providers.ProviderAnthropic, Model: armOpus, Reason: ReasonSessionArmPin,
		DemotedModels: []string{armOpus}, LastServedModel: armOpus, LastTurnEndedAt: time.Now().Add(-time.Minute),
	})

	demotedTurn := runSessionArmTurn(t, svc, ctx, env, feats, uuid.New(), nil)
	assert.Equal(t, armAstra, demotedTurn.Decision.Model)
	assert.False(t, demotedTurn.SessionArmHeld)
	assert.Equal(t, sessionArmOverrideSessionDemoted, demotedTurn.SessionArmOverride)
	require.Len(t, scorer.requests, 1)
	assert.Contains(t, scorer.requests[0].AutomaticExcludedModels, armOpus)
	assert.Equal(t, armOpus, store.armRows()[0].Model, "demotion never rewrites the arm")

	// Cooldown over: the demotion is gone from the thread row.
	thread, _, _ := store.Get(ctx, threadKey, role)
	thread.DemotedModels = nil
	store.seed(threadKey, role, thread)
	returned := runSessionArmTurn(t, svc, ctx, env, feats, uuid.New(), nil)
	assert.Equal(t, armOpus, returned.Decision.Model)
	assert.True(t, returned.SessionArmHeld)
	assert.Len(t, scorer.requests, 1, "the returning turn is served from the arm, not scored")
}

func TestSessionArm_GlobalAutomaticExclusionOverrides(t *testing.T) {
	store := newKeyedPinStore()
	scorer := &authoritativeTestRouter{decision: armDecision(armSonnet)}
	svc := sessionArmService(store, scorer).WithGlobalAutomaticExclusions(
		&stubGlobalExclusionStore{byModel: map[string]string{armOpus: "disabled by operator"}})
	ctx := sessionArmCtx(flags.SessionArmPinMain, "sess-1")
	env, feats := sessionArmMainLoopEnvelope(t, "first prompt")
	store.seed(armKeyFor(ctx, env), sessionArmRole, armPin(armOpus))

	res := runSessionArmTurn(t, svc, ctx, env, feats, uuid.New(), nil)

	assert.Equal(t, armSonnet, res.Decision.Model)
	assert.Equal(t, sessionArmOverrideAutomaticDisabled, res.SessionArmOverride)
	require.Len(t, scorer.requests, 1)
	assert.Contains(t, scorer.requests[0].AutomaticExcludedModels, armOpus)
}

func TestSessionArm_InstallationExclusionOverrides(t *testing.T) {
	store := newKeyedPinStore()
	scorer := &authoritativeTestRouter{decision: armDecision(armSonnet)}
	svc := sessionArmService(store, scorer)
	ctx := context.WithValue(sessionArmCtx(flags.SessionArmPinMain, "sess-1"), InstallationExcludedModelsContextKey{}, []string{armOpus})
	env, feats := sessionArmMainLoopEnvelope(t, "first prompt")
	store.seed(armKeyFor(ctx, env), sessionArmRole, armPin(armOpus))

	res := runSessionArmTurn(t, svc, ctx, env, feats, uuid.New(), func(req *router.Request) {
		req.ExcludedModels = svc.excludedModelsForRequest(ctx)
	})

	assert.Equal(t, armSonnet, res.Decision.Model)
	assert.Equal(t, sessionArmOverrideExcludedModels, res.SessionArmOverride)
}

func TestSessionArm_ProviderNotEnabledOverrides(t *testing.T) {
	store := newKeyedPinStore()
	scorer := &authoritativeTestRouter{decision: armDecision(armLuna)}
	svc := sessionArmService(store, scorer)
	ctx := sessionArmCtx(flags.SessionArmPinMain, "sess-1")
	env, feats := sessionArmMainLoopEnvelope(t, "first prompt")
	store.seed(armKeyFor(ctx, env), sessionArmRole, armPin(armOpus))

	res := runSessionArmTurn(t, svc, ctx, env, feats, uuid.New(), func(req *router.Request) {
		req.EnabledProviders = map[string]struct{}{providers.ProviderOpenAI: {}}
	})

	assert.Equal(t, armLuna, res.Decision.Model)
	assert.Equal(t, sessionArmOverrideProvider, res.SessionArmOverride)
}

func TestSessionArm_ContextWindowOverrides(t *testing.T) {
	const smallWindowArm = "claude-sonnet-4-5" // 200k window, no extended-context beta
	store := newKeyedPinStore()
	scorer := &authoritativeTestRouter{decision: armDecision(armLuna)}
	svc := sessionArmService(store, scorer)
	ctx := sessionArmCtx(flags.SessionArmPinMain, "sess-1")
	// ~300k tokens of history: over the arm's 200k window, inside Luna's 1M.
	env, feats := sessionArmMainLoopEnvelope(t, strings.Repeat("word ", 250_000))
	store.seed(armKeyFor(ctx, env), sessionArmRole, armPin(smallWindowArm))

	// ProxyMessages runs the context pre-filter before the turn loop.
	res := runSessionArmTurn(t, svc, ctx, env, feats, uuid.New(), func(req *router.Request) {
		req.ExcludedModels, _ = excludeContextOverflowModels(env.ContextOverflowTokenEstimate(),
			env.SignatureTokenSavings(), contextWindowOutputReserve, nil, nil, svc.availableModels)
		require.Contains(t, req.ExcludedModels, smallWindowArm)
	})

	assert.Equal(t, armLuna, res.Decision.Model)
	assert.Equal(t, sessionArmOverrideContextWindow, res.SessionArmOverride)
	assert.Equal(t, smallWindowArm, store.armRows()[0].Model)
}

func TestSessionArm_PreFilterExclusionHoldsWhenArmFitsContext(t *testing.T) {
	store := newKeyedPinStore()
	scorer := &authoritativeTestRouter{decision: armDecision(armLuna)}
	svc := sessionArmService(store, scorer)
	ctx := sessionArmCtx(flags.SessionArmPinMain, "sess-1")
	env, feats := sessionArmMainLoopEnvelope(t, "short prompt")
	store.seed(armKeyFor(ctx, env), sessionArmRole, armPin(armOpus))

	// A conservative pre-filter exclusion that the direct fit check clears
	// keeps the arm, as the thread pin's own pin-drop guard does.
	res := runSessionArmTurn(t, svc, ctx, env, feats, uuid.New(), func(req *router.Request) {
		req.ExcludedModels = map[string]struct{}{armOpus: {}}
	})

	assert.Equal(t, armOpus, res.Decision.Model)
	assert.True(t, res.SessionArmHeld)
	assert.Empty(t, res.SessionArmOverride)
}

func TestSessionArm_UnsignedToolHistoryOverridesGeminiArm(t *testing.T) {
	const geminiArm = "gemini-3-pro-preview"
	store := newKeyedPinStore()
	scorer := &authoritativeTestRouter{decision: armDecision(armLuna)}
	svc := sessionArmService(store, scorer)
	ctx := sessionArmCtx(flags.SessionArmPinMain, "sess-1")
	env, feats := sessionArmToolResultEnvelope(t, "first prompt") // tool_use with no thoughtSignature
	require.True(t, env.HasUnsignedToolCallHistory())
	store.seed(armKeyFor(ctx, env), sessionArmRole, sessionpin.Pin{Provider: providers.ProviderGoogle, Model: geminiArm, Reason: ReasonSessionArmPin})

	res := runSessionArmTurn(t, svc, ctx, env, feats, uuid.New(), func(req *router.Request) {
		req.ExcludedModels, _ = excludeGemini3xOnUnsignedHistory(env, nil, map[string]struct{}{geminiArm: {}, armLuna: {}})
		require.Contains(t, req.ExcludedModels, geminiArm)
	})

	assert.Equal(t, armLuna, res.Decision.Model)
	assert.Equal(t, sessionArmOverrideUnsignedHistory, res.SessionArmOverride)
	assert.Equal(t, geminiArm, store.armRows()[0].Model, "the arm survives the override")
}

// --- safety refusal --------------------------------------------------------

func TestSessionArm_RefusalRepinMovesArmOffRefusingModel(t *testing.T) {
	store := newKeyedPinStore()
	svc := sessionArmService(store, &authoritativeTestRouter{decision: armDecision(armOpus)})
	svc.cyberRefusalRepin, svc.cyberRefusalFallbackModel = true, armSonnet
	ctx := context.WithValue(sessionArmCtx(flags.SessionArmPinMain, "sess-1"), InstallationIDContextKey{}, uuid.New().String())
	env, feats := sessionArmMainLoopEnvelope(t, "first prompt")
	store.seed(armKeyFor(ctx, env), sessionArmRole, armPin(armOpus))

	held := runSessionArmTurn(t, svc, ctx, env, feats, uuid.New(), nil)
	require.True(t, held.SessionArmHeld)

	fallback, ok := svc.maybeRepinOnRefusal(ctx, &refusalObserver{refused: true}, held.SessionKey, stickyStateRole(held), held.Decision)
	require.True(t, ok)
	svc.repinSessionArmOffRefusingModel(ctx, held, held.Decision, fallback)

	rows := store.armRows()
	require.Len(t, rows, 1)
	assert.Equal(t, armSonnet, rows[0].Model)
	assert.Equal(t, providers.ProviderAnthropic, rows[0].Provider)

	next := runSessionArmTurn(t, svc, ctx, env, feats, uuid.New(), nil)
	assert.Equal(t, armSonnet, next.Decision.Model, "the next covered turn must not return to the refusing arm")
	assert.True(t, next.SessionArmHeld)
}

func TestSessionArm_RefusalOnRescuedStandInLeavesArm(t *testing.T) {
	store := newKeyedPinStore()
	svc := sessionArmService(store, &authoritativeTestRouter{decision: armDecision(armOpus)})
	svc.cyberRefusalRepin, svc.cyberRefusalFallbackModel = true, armSonnet
	ctx := context.WithValue(sessionArmCtx(flags.SessionArmPinMain, "sess-1"), InstallationIDContextKey{}, uuid.New().String())
	env, feats := sessionArmMainLoopEnvelope(t, "first prompt")
	store.seed(armKeyFor(ctx, env), sessionArmRole, armPin(armOpus))

	held := runSessionArmTurn(t, svc, ctx, env, feats, uuid.New(), nil)
	standIn := armDecision(armAstra)
	standIn.Reason = ReasonSiblingFailover

	fallback, ok := svc.maybeRepinOnRefusal(ctx, &refusalObserver{refused: true}, held.SessionKey, stickyStateRole(held), standIn)
	require.True(t, ok)
	svc.repinSessionArmOffRefusingModel(ctx, held, standIn, fallback)

	rows := store.armRows()
	require.Len(t, rows, 1)
	assert.Equal(t, armOpus, rows[0].Model)
}

// --- flag off --------------------------------------------------------------

func TestSessionArm_FlagOffLeavesRoutingUntouched(t *testing.T) {
	run := func(ctx context.Context) (turnLoopResult, *keyedPinStore, *authoritativeTestRouter) {
		store := newKeyedPinStore()
		scorer := &authoritativeTestRouter{decision: armDecision(armSonnet)}
		svc := sessionArmService(store, scorer)
		env, feats := sessionArmMainLoopEnvelope(t, "first prompt")
		// An arm row left over from a previous flag-on session must be ignored.
		store.seed(armKeyFor(ctx, env), sessionArmRole, armPin(armOpus))
		res := runSessionArmTurn(t, svc, ctx, env, feats, uuid.New(), nil)
		return res, store, scorer
	}
	offRes, offStore, offScorer := run(sessionArmCtx(flags.SessionArmPinOff, "sess-1"))
	noneRes, noneStore, noneScorer := run(sessionArmCtx("", "sess-1"))

	for _, res := range []turnLoopResult{offRes, noneRes} {
		assert.Equal(t, armSonnet, res.Decision.Model)
		assert.Empty(t, res.SessionArmMode)
		assert.False(t, res.SessionArmHeld)
		assert.False(t, res.SessionArmAnchored)
		assert.Nil(t, sessionArmLogFields(res))
	}
	assert.Len(t, offScorer.requests, 1)
	assert.Len(t, noneScorer.requests, 1)
	assert.Equal(t, offRes.Decision, noneRes.Decision)
	assert.Equal(t, offRes.PinTier, noneRes.PinTier)
	assert.Len(t, offStore.armRows(), 1)
	assert.Len(t, noneStore.armRows(), 1)
}

// --- sub-agent threads -----------------------------------------------------

func TestSessionArm_MainModeLeavesSubAgentsOnPerTurnRouting(t *testing.T) {
	store := newKeyedPinStore()
	scorer := &authoritativeTestRouter{decision: armDecision(armSonnet)}
	svc := sessionArmService(store, scorer)
	ctx := sessionArmCtx(flags.SessionArmPinMain, "sess-1")
	mainEnv, _ := sessionArmMainLoopEnvelope(t, "first prompt")
	store.seed(armKeyFor(ctx, mainEnv), sessionArmRole, armPin(armOpus))
	subEnv, subFeats := sessionArmSubAgentEnvelope(t, "explore the repo")

	res := runSessionArmTurn(t, svc, ctx, subEnv, subFeats, uuid.New(), nil)

	assert.Equal(t, armSonnet, res.Decision.Model)
	assert.Empty(t, res.SessionArmMode)
	assert.Len(t, scorer.requests, 1)
	assert.Len(t, store.armRows(), 1)
}

func TestSessionArm_AllModeSubAgentsInheritMainArm(t *testing.T) {
	store := newKeyedPinStore()
	scorer := &authoritativeTestRouter{decision: armDecision(armSonnet)}
	svc := sessionArmService(store, scorer)
	ctx := sessionArmCtx(flags.SessionArmPinAll, "sess-1")
	mainEnv, _ := sessionArmMainLoopEnvelope(t, "first prompt")
	store.seed(armKeyFor(ctx, mainEnv), sessionArmRole, armPin(armOpus))

	for _, prompt := range []string{"explore the repo", "write the tests"} {
		subEnv, subFeats := sessionArmSubAgentEnvelope(t, prompt)
		assert.Equal(t, armKeyFor(ctx, mainEnv), armKeyFor(ctx, subEnv), "same client session shares one arm")
		assert.NotEqual(t, deriveSessionKey(mainEnv, "api-key", "sess-1"), deriveSessionKey(subEnv, "api-key", "sess-1"), "thread keys stay independent")
		res := runSessionArmTurn(t, svc, ctx, subEnv, subFeats, uuid.New(), nil)
		assert.Equal(t, armOpus, res.Decision.Model)
		assert.True(t, res.SessionArmHeld)
		assert.Equal(t, flags.SessionArmPinAll, res.SessionArmMode)
	}
	assert.Empty(t, scorer.requests)
}

func TestSessionArm_AllModeSubAgentNeverAnchors(t *testing.T) {
	store := newKeyedPinStore()
	scorer := &authoritativeTestRouter{decision: armDecision(armSonnet)}
	svc := sessionArmService(store, scorer)
	ctx := sessionArmCtx(flags.SessionArmPinAll, "sess-1")
	subEnv, subFeats := sessionArmSubAgentEnvelope(t, "explore the repo")

	res := runSessionArmTurn(t, svc, ctx, subEnv, subFeats, uuid.New(), nil)

	assert.Equal(t, armSonnet, res.Decision.Model)
	assert.False(t, res.SessionArmAnchored)
	assert.Empty(t, store.armRows(), "only the main thread assigns the arm")
}

func TestSessionArm_OtherClientSessionHasItsOwnArm(t *testing.T) {
	store := newKeyedPinStore()
	scorer := &authoritativeTestRouter{decision: armDecision(armSonnet)}
	svc := sessionArmService(store, scorer)
	ctx1 := sessionArmCtx(flags.SessionArmPinMain, "sess-1")
	ctx2 := sessionArmCtx(flags.SessionArmPinMain, "sess-2")
	env, feats := sessionArmMainLoopEnvelope(t, "first prompt")
	store.seed(armKeyFor(ctx1, env), sessionArmRole, armPin(armOpus))

	res := runSessionArmTurn(t, svc, ctx2, env, feats, uuid.New(), nil)

	assert.Equal(t, armSonnet, res.Decision.Model)
	assert.True(t, res.SessionArmAnchored)
	assert.Len(t, store.armRows(), 2)
}

// --- flag plumbing ---------------------------------------------------------

func TestResolveSessionArmPin_DeploymentDefaultAndOrgOverride(t *testing.T) {
	svc := sessionArmService(newKeyedPinStore(), nil)
	assert.Equal(t, flags.SessionArmPinOff, svc.ResolveSessionArmPin(context.Background()))

	svc.WithSessionArmPin(flags.SessionArmPinMain)
	assert.Equal(t, flags.SessionArmPinMain, svc.ResolveSessionArmPin(context.Background()))
	assert.Equal(t, flags.SessionArmPinAll, svc.ResolveSessionArmPin(sessionArmCtx(flags.SessionArmPinAll, "")))
	assert.Equal(t, flags.SessionArmPinOff, svc.ResolveSessionArmPin(sessionArmCtx(flags.SessionArmPinOff, "")))

	bogus := flags.WithOverrides(context.Background(), flags.Overrides{Strings: map[flags.Key]string{flags.KeySessionArmPin: "sometimes"}})
	assert.Equal(t, flags.SessionArmPinOff, svc.ResolveSessionArmPin(bogus), "an unparseable override fails closed to off")
}

func TestSessionArmLogFields(t *testing.T) {
	fields := sessionArmLogFields(turnLoopResult{
		SessionArmMode: flags.SessionArmPinMain, SessionArmModel: armOpus, SessionArmHeld: true,
	})
	assert.Equal(t, []any{
		"session_arm_mode", "main",
		"session_arm_model", armOpus,
		"session_arm_held", true,
		"session_arm_anchored", false,
		"session_arm_override", "",
	}, fields)
}
