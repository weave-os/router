package proxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// multiBindingModel is served by Bedrock (primary) and OpenRouter (fallback),
// so excluding one provider must not be enough to refuse a force of it.
const multiBindingModel = "qwen/qwen3-235b-a22b-2507"

func excludedProvidersCtx(names ...string) context.Context {
	return context.WithValue(context.Background(),
		InstallationExcludedProvidersContextKey{}, names)
}

func keyed(names ...string) map[string]struct{} {
	out := make(map[string]struct{}, len(names))
	for _, n := range names {
		out[n] = struct{}{}
	}
	return out
}

func forceCommandEnv(t *testing.T) *translate.RequestEnvelope {
	t.Helper()
	env, err := translate.ParseAnthropic([]byte(`{
		"model":"claude-opus-4-8",
		"messages":[{"role":"user","content":"hi"}]
	}`))
	require.NoError(t, err)
	return env
}

// TestForceModelCommand_RejectsSoleProviderExcluded: core case — pinning
// would have reported success, then served a different model every turn.
func TestForceModelCommand_RejectsSoleProviderExcluded(t *testing.T) {
	store := &recordingPinStore{}
	svc := NewService(nil, nil, nil, false, nil, store, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil).
		WithDeploymentKeyedProviders(keyed(providers.ProviderAnthropic))

	env := forceCommandEnv(t)
	rec := httptest.NewRecorder()
	require.NoError(t, svc.handleForceModelCommand(
		excludedProvidersCtx(providers.ProviderAnthropic), rec, env,
		translate.ForceModelResult{Model: "opus"},
		uuid.New(), DeriveSessionKey(env, "key-1"), DeriveSessionKey(env, "key-1"), 10))

	assert.Empty(t, store.upserts, "a refused force must not write a pin")
	assert.Contains(t, rec.Body.String(), "force-model rejected")
	assert.Contains(t, rec.Body.String(), providers.ProviderAnthropic,
		"the caller must be told which provider is excluded")
}

// TestForceModelCommand_RejectsExcludedModel covers the model-level list, which
// is enforced independently of the provider list.
func TestForceModelCommand_RejectsExcludedModel(t *testing.T) {
	store := &recordingPinStore{}
	svc := NewService(nil, nil, nil, false, nil, store, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil).
		WithDeploymentKeyedProviders(keyed(providers.ProviderAnthropic))

	ctx := context.WithValue(context.Background(),
		InstallationExcludedModelsContextKey{}, []string{"claude-opus-5"})
	env := forceCommandEnv(t)
	rec := httptest.NewRecorder()
	require.NoError(t, svc.handleForceModelCommand(ctx, rec, env,
		translate.ForceModelResult{Model: "opus"},
		uuid.New(), DeriveSessionKey(env, "key-1"), DeriveSessionKey(env, "key-1"), 10))

	assert.Empty(t, store.upserts, "an excluded model must not be pinned")
	assert.Contains(t, rec.Body.String(), "force-model rejected")
}

// TestForceModelCommand_AllowsWhenOneBindingSurvives guards against over-
// rejecting: a multi-binding model is only refused when every binding is gone.
func TestForceModelCommand_AllowsWhenOneBindingSurvives(t *testing.T) {
	store := &recordingPinStore{}
	svc := NewService(nil, nil, nil, false, nil, store, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil).
		WithDeploymentKeyedProviders(keyed(providers.ProviderBedrock, providers.ProviderOpenRouter))

	env := forceCommandEnv(t)
	rec := httptest.NewRecorder()
	require.NoError(t, svc.handleForceModelCommand(
		excludedProvidersCtx(providers.ProviderOpenRouter), rec, env,
		translate.ForceModelResult{Model: multiBindingModel},
		uuid.New(), DeriveSessionKey(env, "key-1"), DeriveSessionKey(env, "key-1"), 10))

	require.Len(t, store.upserts, 1, "one excluded binding must not refuse the force")
	assert.Equal(t, multiBindingModel, store.upserts[0].Model)
	assert.Contains(t, rec.Body.String(), "force-model applied")
}

// TestForceModelCommand_RejectsWhenEveryBindingExcluded is the case the fence
// was built for, expressed through exclusions alone.
func TestForceModelCommand_RejectsWhenEveryBindingExcluded(t *testing.T) {
	store := &recordingPinStore{}
	svc := NewService(nil, nil, nil, false, nil, store, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil).
		WithDeploymentKeyedProviders(keyed(providers.ProviderBedrock, providers.ProviderOpenRouter))

	env := forceCommandEnv(t)
	rec := httptest.NewRecorder()
	require.NoError(t, svc.handleForceModelCommand(
		excludedProvidersCtx(providers.ProviderBedrock, providers.ProviderOpenRouter),
		rec, env, translate.ForceModelResult{Model: multiBindingModel},
		uuid.New(), DeriveSessionKey(env, "key-1"), DeriveSessionKey(env, "key-1"), 10))

	assert.Empty(t, store.upserts)
	assert.Contains(t, rec.Body.String(), "force-model rejected")
}

// TestForceModelCommand_SessionStrikeOutDoesNotReject keeps transient 529
// evidence from masquerading as operator policy.
func TestForceModelCommand_SessionStrikeOutDoesNotReject(t *testing.T) {
	store := &recordingPinStore{}
	svc := NewService(nil, nil, nil, false, nil, store, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil).
		WithDeploymentKeyedProviders(keyed(providers.ProviderAnthropic))

	ctx := context.WithValue(context.Background(),
		SessionDisabledProvidersContextKey{}, []string{providers.ProviderAnthropic})
	env := forceCommandEnv(t)
	rec := httptest.NewRecorder()
	require.NoError(t, svc.handleForceModelCommand(ctx, rec, env,
		translate.ForceModelResult{Model: "opus"},
		uuid.New(), DeriveSessionKey(env, "key-1"), DeriveSessionKey(env, "key-1"), 10))

	require.Len(t, store.upserts, 1,
		"a provider struck out for overload is not an exclusion")
}

// TestForceModelHeader_RejectsExcludedModel: the headless path has no synthetic
// response to warn through, so it fails the request instead.
func TestForceModelHeader_RejectsExcludedModel(t *testing.T) {
	store := &recordingPinStore{}
	svc := NewService(nil, nil, nil, false, nil, store, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil).
		WithDeploymentKeyedProviders(keyed(providers.ProviderAnthropic))

	env := forceCommandEnv(t)
	req, err := http.NewRequest(http.MethodPost, "/v1/messages", nil)
	require.NoError(t, err)
	req.Header.Set(ForceModelHeader, "opus")

	_, model, forceErr := svc.applyForceModelHeader(
		excludedProvidersCtx(providers.ProviderAnthropic), req,
		uuid.New(), DeriveSessionKey(env, "key-1"))

	require.Error(t, forceErr)
	assert.True(t, errors.Is(forceErr, ErrForcedModelExcluded))
	assert.Empty(t, model)
	assert.Empty(t, store.upserts, "a refused header force must not write a pin")
}

// TestForceModelHeader_UnfencedInstallationUnaffected pins the regression
// boundary: no exclusions means the header behaves exactly as before.
func TestForceModelHeader_UnfencedInstallationUnaffected(t *testing.T) {
	store := &recordingPinStore{}
	svc := NewService(nil, nil, nil, false, nil, store, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil)

	env := forceCommandEnv(t)
	req, err := http.NewRequest(http.MethodPost, "/v1/messages", nil)
	require.NoError(t, err)
	req.Header.Set(ForceModelHeader, "opus")

	_, model, forceErr := svc.applyForceModelHeader(
		context.Background(), req, uuid.New(), DeriveSessionKey(env, "key-1"))

	require.NoError(t, forceErr)
	assert.Equal(t, "claude-opus-5", model)
	require.Len(t, store.upserts, 1)
}

// The `:level` suffix must reach the caller's context, not only *r: the
// dispatch path reads routingKnobsForRequest(ctx) from the context it was
// already carrying, so a knob written only onto r.Context() never made it
// into the upstream body.
func TestForceModelHeader_EffortSuffixLandsOnReturnedContext(t *testing.T) {
	store := &recordingPinStore{}
	svc := NewService(nil, nil, nil, false, nil, store, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil)

	env := forceCommandEnv(t)
	req, err := http.NewRequest(http.MethodPost, "/v1/messages", nil)
	require.NoError(t, err)
	req.Header.Set(ForceModelHeader, "opus:xhigh")

	ctx, model, forceErr := svc.applyForceModelHeader(
		context.Background(), req, uuid.New(), DeriveSessionKey(env, "key-1"))

	require.NoError(t, forceErr)
	assert.Equal(t, "claude-opus-5", model)
	knobs := routingKnobsForRequest(ctx)
	require.NotNil(t, knobs)
	assert.Equal(t, "xhigh", knobs.ForceEffort)
}

// TestTurnLoop_ForcedPinToNewlyExcludedProviderRejects: policy can change
// mid-session; the pre-exclusion pin would otherwise silently re-route.
func TestTurnLoop_ForcedPinToNewlyExcludedProviderRejects(t *testing.T) {
	fr := &tierProbeRouter{available: map[string]struct{}{"claude-haiku-4-5": {}}}
	store := &overwritingPinStore{pin: sessionpin.Pin{
		Provider:    providers.ProviderAnthropic,
		Model:       "claude-opus-5",
		Reason:      translate.ReasonUserForceModel,
		PinnedUntil: pinNeverExpires,
	}, found: true}
	svc := NewService(fr, nil, nil, false, nil, store, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil).
		WithDeploymentKeyedProviders(keyed(providers.ProviderAnthropic))

	env := forceCommandEnv(t)
	feats := env.RoutingFeatures(false)
	_, err := svc.runTurnLoop(
		excludedProvidersCtx(providers.ProviderAnthropic),
		env, feats, "key-1", uuid.New(), "", nil,
		router.Request{RequestedModel: feats.Model})

	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrForcedModelExcluded))
	assert.Empty(t, fr.captured,
		"the request must fail rather than quietly re-route through the scorer")
}

// TestTurnLoop_AutomaticPinToExcludedProviderStillFallsThrough: only an
// explicit user force fails the request; automatic pins degrade gracefully.
func TestTurnLoop_AutomaticPinToExcludedProviderStillFallsThrough(t *testing.T) {
	fr := &tierProbeRouter{available: map[string]struct{}{"claude-haiku-4-5": {}}}
	store := &overwritingPinStore{pin: sessionpin.Pin{
		Provider:    providers.ProviderAnthropic,
		Model:       "claude-opus-5",
		Reason:      "cluster:v0.2",
		PinnedUntil: time.Now().Add(time.Hour),
	}, found: true}
	svc := NewService(fr, nil, nil, false, nil, store, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil).
		WithDeploymentKeyedProviders(keyed(providers.ProviderAnthropic))

	env := forceCommandEnv(t)
	feats := env.RoutingFeatures(false)
	_, err := svc.runTurnLoop(
		excludedProvidersCtx(providers.ProviderAnthropic),
		env, feats, "key-1", uuid.New(), "", nil,
		router.Request{RequestedModel: feats.Model})

	require.NoError(t, err, "an automatic pin must not fail the request")
}

// TestForceModelCommand_AllowsWhenAnotherKeyedBindingServes: excluding one
// provider refuses a force only when no other keyed binding can serve it.
func TestForceModelCommand_AllowsWhenAnotherKeyedBindingServes(t *testing.T) {
	store := &recordingPinStore{}
	svc := NewService(nil, nil, nil, false, nil, store, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil).
		WithDeploymentKeyedProviders(keyed(
			providers.ProviderAnthropic, providers.ProviderAnthropicGateway))

	env := forceCommandEnv(t)
	rec := httptest.NewRecorder()
	require.NoError(t, svc.handleForceModelCommand(
		excludedProvidersCtx(providers.ProviderAnthropic), rec, env,
		translate.ForceModelResult{Model: "opus"},
		uuid.New(), DeriveSessionKey(env, "key-1"), DeriveSessionKey(env, "key-1"), 10))

	require.Len(t, store.upserts, 1)
	assert.Contains(t, rec.Body.String(), "force-model applied")
	// Forcing resolves to the primary (excluded) binding, so pinning it
	// verbatim would lose the pin to eligibility on the next turn.
	assert.Equal(t, providers.ProviderAnthropicGateway, store.upserts[0].Provider,
		"the pin must name the permitted binding, not the excluded primary")
}

// TestForceModelCommand_RejectsDeploymentExcludedModel: ROUTER_EXCLUDED_MODELS
// is as authoritative as the per-installation list.
func TestForceModelCommand_RejectsDeploymentExcludedModel(t *testing.T) {
	store := &recordingPinStore{}
	svc := NewService(nil, nil, nil, false, nil, store, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil).
		WithDeploymentKeyedProviders(keyed(providers.ProviderAnthropic)).
		WithExcludedModelsOverride([]string{"claude-opus-5"})

	env := forceCommandEnv(t)
	rec := httptest.NewRecorder()
	require.NoError(t, svc.handleForceModelCommand(context.Background(), rec, env,
		translate.ForceModelResult{Model: "opus"},
		uuid.New(), DeriveSessionKey(env, "key-1"), DeriveSessionKey(env, "key-1"), 10))

	assert.Empty(t, store.upserts, "an env-excluded model must not be pinned")
	assert.Contains(t, rec.Body.String(), "force-model rejected")
}

// TestTurnLoop_ForcedPinFollowsSurvivingBinding: excluding the pinned provider
// must move the pin to a permitted binding, not silently drop it to the scorer.
func TestTurnLoop_ForcedPinFollowsSurvivingBinding(t *testing.T) {
	fr := &tierProbeRouter{available: map[string]struct{}{"claude-haiku-4-5": {}}}
	store := &overwritingPinStore{pin: sessionpin.Pin{
		Provider:    providers.ProviderAnthropic,
		Model:       "claude-opus-5",
		Reason:      translate.ReasonUserForceModel,
		PinnedUntil: pinNeverExpires,
	}, found: true}
	svc := NewService(fr, nil, nil, false, nil, store, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil).
		WithDeploymentKeyedProviders(keyed(
			providers.ProviderAnthropic, providers.ProviderAnthropicGateway))

	env := forceCommandEnv(t)
	feats := env.RoutingFeatures(false)
	res, err := svc.runTurnLoop(
		excludedProvidersCtx(providers.ProviderAnthropic),
		env, feats, "key-1", uuid.New(), "", nil,
		router.Request{
			RequestedModel:   feats.Model,
			EnabledProviders: keyed(providers.ProviderAnthropicGateway),
		})

	require.NoError(t, err)
	assert.True(t, res.StickyHit, "the force must survive on the permitted binding")
	assert.Equal(t, providers.ProviderAnthropicGateway, res.Decision.Provider)
	assert.Equal(t, "claude-opus-5", res.Decision.Model)
	assert.Empty(t, fr.captured, "the pin must not fall through to the scorer")
}

// TestTurnLoop_StrikeExemptionCoversRemappedBinding: strike exemption must
// cover the remapped binding, or a 529 strike on it vetoes the force.
func TestTurnLoop_StrikeExemptionCoversRemappedBinding(t *testing.T) {
	fr := &tierProbeRouter{available: map[string]struct{}{"claude-haiku-4-5": {}}}
	store := &overwritingPinStore{pin: sessionpin.Pin{
		Provider:          providers.ProviderAnthropic,
		Model:             "claude-opus-5",
		Reason:            translate.ReasonUserForceModel,
		PinnedUntil:       pinNeverExpires,
		DisabledProviders: []string{providers.ProviderAnthropicGateway},
	}, found: true}
	svc := NewService(fr, nil, nil, false, nil, store, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil).
		WithDeploymentKeyedProviders(keyed(
			providers.ProviderAnthropic, providers.ProviderAnthropicGateway))

	env := forceCommandEnv(t)
	feats := env.RoutingFeatures(false)
	res, err := svc.runTurnLoop(
		excludedProvidersCtx(providers.ProviderAnthropic),
		env, feats, "key-1", uuid.New(), "", nil,
		router.Request{
			RequestedModel:   feats.Model,
			EnabledProviders: keyed(providers.ProviderAnthropicGateway),
		})

	require.NoError(t, err)
	assert.True(t, res.StickyHit, "a session strike must not veto an explicit force")
	assert.Equal(t, providers.ProviderAnthropicGateway, res.Decision.Provider)
}

// TestTurnLoop_HardPinnedTurnForcedPinFollowsSurvivingBinding: the hard-pin
// fast path needs the same remap or the force loses probe/compaction turns.
func TestTurnLoop_HardPinnedTurnForcedPinFollowsSurvivingBinding(t *testing.T) {
	fr := &tierProbeRouter{available: map[string]struct{}{"claude-haiku-4-5": {}}}
	store := &overwritingPinStore{pin: sessionpin.Pin{
		Provider:    providers.ProviderAnthropic,
		Model:       "claude-opus-5",
		Reason:      translate.ReasonUserForceModel,
		PinnedUntil: pinNeverExpires,
	}, found: true}
	svc := NewService(fr, nil, nil, false, nil, store, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil).
		WithDeploymentKeyedProviders(keyed(
			providers.ProviderAnthropic, providers.ProviderAnthropicGateway))

	env, err := translate.ParseAnthropic([]byte(
		`{"model":"claude-opus-4-8","max_tokens":1,"messages":[{"role":"user","content":"quota"}]}`))
	require.NoError(t, err)
	feats := env.RoutingFeatures(false)
	res, err := svc.runTurnLoop(
		excludedProvidersCtx(providers.ProviderAnthropic),
		env, feats, "key-1", uuid.New(), "", nil,
		router.Request{
			RequestedModel:   feats.Model,
			EnabledProviders: keyed(providers.ProviderAnthropicGateway),
		})

	require.NoError(t, err)
	require.Equal(t, turntype.Probe, res.TurnType, "fixture must exercise the hard-pinned path")
	assert.False(t, res.HardPinned, "the force outranks the hard pin")
	assert.Equal(t, "claude-opus-5", res.Decision.Model)
	assert.Equal(t, providers.ProviderAnthropicGateway, res.Decision.Provider)
}

func TestClassifyDispatchError_ForcedModelExcluded(t *testing.T) {
	cls, ok := ClassifyDispatchError(&ForcedModelExcludedError{
		Model:  "claude-opus-5",
		Reason: "claude-opus-5 is only served by anthropic, which is excluded on this installation",
	})

	require.True(t, ok)
	assert.Equal(t, http.StatusBadRequest, cls.Status)
	assert.True(t, cls.Kind.IsClientError(),
		"an excluded force is a client-input problem, not an upstream failure")
	assert.Contains(t, cls.Message, "claude-opus-5")
	assert.Contains(t, cls.Message, "/unforce-model")
}

// Desugaring only covers routableUniverse; claude-opus-4-8 is passthrough-only
// and never in that set, so the allowlist check in forcedModelBinding must be direct.
func TestForcedModelBinding_RejectsPassthroughModelOutsideAllowlist(t *testing.T) {
	svc := NewService(nil, nil, nil, false, nil, nil, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil).
		WithDeploymentKeyedProviders(keyed(providers.ProviderAnthropic)).
		// nil availableModels enumerates the full catalog and masks the passthrough-
		// only bypass; must be a routing-targets-only universe.
		WithAvailableModels(map[string]struct{}{"claude-opus-5": {}})

	ctx := context.WithValue(context.Background(),
		InstallationAllowedModelsContextKey{}, []string{"claude-opus-5"})

	binding, reason := svc.forcedModelBinding(ctx, "claude-opus-4-8", providers.ProviderAnthropic)

	assert.Empty(t, binding, "a passthrough model outside the allowlist must not resolve a binding")
	assert.Contains(t, reason, "allowed-model list",
		"the refusal must name the allowlist, not a generic exclusion")
}

// The allowlist must only ever narrow: an allowlisted model still forces fine.
func TestForcedModelBinding_AllowsPassthroughModelInsideAllowlist(t *testing.T) {
	svc := NewService(nil, nil, nil, false, nil, nil, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil).
		WithDeploymentKeyedProviders(keyed(providers.ProviderAnthropic))

	ctx := context.WithValue(context.Background(),
		InstallationAllowedModelsContextKey{}, []string{"claude-opus-4-8"})

	binding, reason := svc.forcedModelBinding(ctx, "claude-opus-4-8", providers.ProviderAnthropic)

	assert.Empty(t, reason)
	assert.Equal(t, providers.ProviderAnthropic, binding)
}

// No allowlist configured = no restriction, so passthrough forcing is unchanged.
func TestForcedModelBinding_NoAllowlistLeavesPassthroughForcingUnchanged(t *testing.T) {
	svc := NewService(nil, nil, nil, false, nil, nil, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil).
		WithDeploymentKeyedProviders(keyed(providers.ProviderAnthropic))

	binding, reason := svc.forcedModelBinding(context.Background(), "claude-opus-4-8", providers.ProviderAnthropic)

	assert.Empty(t, reason)
	assert.Equal(t, providers.ProviderAnthropic, binding)
}

func gatewayKeyCtx(model string) context.Context {
	return context.WithValue(context.Background(), ExternalAPIKeysContextKey{},
		[]*auth.ExternalAPIKey{{
			Provider:     providers.ProviderAnthropicGateway,
			Plaintext:    []byte("pat"),
			ModelAliases: map[string]string{model: "upstream-name"},
		}})
}

// Gateway-exclusive routing drops the vendor primary, so a force resolved to
// it would be rejected by the pin check and route automatically instead.
func TestForcedModelBinding_GatewayExclusivePinsTheGateway(t *testing.T) {
	svc := NewService(nil, nil, nil, false, nil, nil, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil).
		WithDeploymentKeyedProviders(keyed(providers.ProviderAnthropic))

	binding, reason := svc.forcedModelBinding(
		gatewayKeyCtx("claude-opus-5"), "claude-opus-5", providers.ProviderAnthropic)

	assert.Empty(t, reason)
	assert.Equal(t, providers.ProviderAnthropicGateway, binding)
}

// A gateway serves only what its aliases name, so forcing anything else must
// be refused loudly rather than pinned to an unroutable vendor.
func TestForcedModelBinding_GatewayExclusiveRefusesUnaliasedModel(t *testing.T) {
	svc := NewService(nil, nil, nil, false, nil, nil, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil).
		WithDeploymentKeyedProviders(keyed(providers.ProviderAnthropic))

	binding, reason := svc.forcedModelBinding(
		gatewayKeyCtx("claude-opus-5"), "claude-haiku-4-5", providers.ProviderAnthropic)

	assert.Empty(t, binding)
	assert.Contains(t, reason, "gateway keys")
}
