package proxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"workweave/router/internal/providers"
	"workweave/router/internal/router"
	"workweave/router/internal/router/sessionpin"
	"workweave/router/internal/translate"

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

// TestForceModelCommand_RejectsSoleProviderExcluded is the core of authoritative
// exclusions: pinning would have reported success and then served something
// else on every later turn.
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
		uuid.New(), DeriveSessionKey(env, "key-1"), 10))

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
		uuid.New(), DeriveSessionKey(env, "key-1"), 10))

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
		uuid.New(), DeriveSessionKey(env, "key-1"), 10))

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
		uuid.New(), DeriveSessionKey(env, "key-1"), 10))

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
		uuid.New(), DeriveSessionKey(env, "key-1"), 10))

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

	model, forceErr := svc.applyForceModelHeader(
		excludedProvidersCtx(providers.ProviderAnthropic), req, env,
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

	model, forceErr := svc.applyForceModelHeader(
		context.Background(), req, env, uuid.New(), DeriveSessionKey(env, "key-1"))

	require.NoError(t, forceErr)
	assert.Equal(t, "claude-opus-5", model)
	require.Len(t, store.upserts, 1)
}

// TestTurnLoop_ForcedPinToNewlyExcludedProviderRejects covers policy changing
// under a live session: the pin predates the exclusion, and silently reverting
// to automatic routing is what made this worth rejecting.
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
// explicit user force is authoritative enough to fail the request; an automatic
// pin must keep degrading gracefully.
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

// TestForceModelCommand_AllowsWhenAnotherKeyedBindingServes: excluding a
// provider only refuses the force when no other keyed binding can serve the
// model. claude-opus-5 is also bound to the Anthropic-compatible gateway.
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
		uuid.New(), DeriveSessionKey(env, "key-1"), 10))

	require.Len(t, store.upserts, 1)
	assert.Contains(t, rec.Body.String(), "force-model applied")
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
