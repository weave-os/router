package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"workweave/router/internal/providers"
	"workweave/router/internal/router"
	"workweave/router/internal/router/sessionpin"
	"workweave/router/internal/translate"
)

// The `:level` suffix of a /force-model command must be persisted on the pin;
// later turns have no header or command to recover it from.
func TestForceModelCommand_PersistsEffortOnPin(t *testing.T) {
	store := &recordingPinStore{}
	svc := NewService(nil, nil, nil, false, nil, store, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil)

	env := forceCommandEnv(t)
	rec := httptest.NewRecorder()
	require.NoError(t, svc.handleForceModelCommand(context.Background(), rec, env,
		translate.ForceModelResult{Model: "opus:xhigh"},
		uuid.New(), DeriveSessionKey(env, "key-1"), DeriveSessionKey(env, "key-1"), 10))

	require.Len(t, store.upserts, 1)
	assert.Equal(t, "claude-opus-5", store.upserts[0].Model)
	assert.Equal(t, "xhigh", store.upserts[0].Effort)
	assert.Contains(t, rec.Body.String(), "claude-opus-5:xhigh")
}

// The tool-result form of the command dispatches on the same turn through
// req.ForceModel, so the returned spec must keep the suffix.
func TestApplyForceModelCommand_ReturnsEffortQualifiedSpec(t *testing.T) {
	store := &recordingPinStore{}
	svc := NewService(nil, nil, nil, false, nil, store, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil)

	env := forceCommandEnv(t)
	spec, _, err := svc.applyForceModelCommand(context.Background(), env,
		translate.ForceModelResult{Model: "opus:xhigh", FromToolResult: true},
		uuid.New(), DeriveSessionKey(env, "key-1"), DeriveSessionKey(env, "key-1"))
	require.NoError(t, err)
	assert.Equal(t, "claude-opus-5:xhigh", spec)

	model, _, known, effort := resolveForceModelWithEffort(spec)
	require.True(t, known)
	assert.Equal(t, "claude-opus-5", model)
	assert.Equal(t, "xhigh", effort)
}

func TestForceModelHeader_PersistsEffortOnPin(t *testing.T) {
	store := &recordingPinStore{}
	svc := NewService(nil, nil, nil, false, nil, store, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil)

	env := forceCommandEnv(t)
	req, err := http.NewRequest(http.MethodPost, "/v1/messages", nil)
	require.NoError(t, err)
	req.Header.Set(ForceModelHeader, "opus:high")

	_, _, forceErr := svc.applyForceModelHeader(
		context.Background(), req, uuid.New(), DeriveSessionKey(env, "key-1"))
	require.NoError(t, forceErr)

	require.Len(t, store.upserts, 1)
	assert.Equal(t, "high", store.upserts[0].Effort)
}

// A turn served from a stored force-model pin must rehydrate the pinned effort
// onto the decision so dispatch emits it.
func TestRunTurnLoop_ForcedPinRehydratesEffort(t *testing.T) {
	store := &forcedPinStore{pin: sessionpin.Pin{
		Provider:    providers.ProviderAnthropic,
		Model:       "claude-opus-4-8",
		Effort:      "xhigh",
		Reason:      translate.ReasonUserForceModel,
		PinnedUntil: time.Now().Add(time.Hour),
	}}
	fr := &tierProbeRouter{available: map[string]struct{}{"claude-haiku-4-5": {}}}
	svc := NewService(fr, nil, nil, false, nil, store, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil)

	env, err := translate.ParseAnthropic([]byte(`{
		"model":"claude-opus-4-8",
		"system":"Your task is to create a detailed summary of the conversation so far.",
		"messages":[{"role":"user","content":"summarize"}]
	}`))
	require.NoError(t, err)
	feats := env.RoutingFeatures(false)

	res, err := svc.runTurnLoop(context.Background(), env, feats, "key-1", uuid.New(), "", nil, router.Request{
		RequestedModel: feats.Model,
	})
	require.NoError(t, err)

	assert.Equal(t, "claude-opus-4-8", res.Decision.Model)
	assert.Equal(t, "xhigh", res.Decision.Effort)
	assert.Equal(t, "xhigh",
		svc.resolveEffort(context.Background(), res.Decision,
			router.Lookup(res.Decision.Model), false).Selected)
}

// req.ForceModel (the header path) carries its own suffix into the in-memory
// forced pin for the current turn.
func TestRunTurnLoop_RequestForceModelCarriesEffort(t *testing.T) {
	store := &forcedPinStore{}
	fr := &tierProbeRouter{available: map[string]struct{}{"claude-haiku-4-5": {}}}
	svc := NewService(fr, nil, nil, false, nil, store, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil)

	env, err := translate.ParseAnthropic([]byte(`{
		"model":"claude-opus-4-8",
		"system":"Your task is to create a detailed summary of the conversation so far.",
		"messages":[{"role":"user","content":"summarize"}]
	}`))
	require.NoError(t, err)
	feats := env.RoutingFeatures(false)

	res, err := svc.runTurnLoop(context.Background(), env, feats, "key-1", uuid.New(), "", nil, router.Request{
		RequestedModel: feats.Model,
		ForceModel:     "opus:medium",
	})
	require.NoError(t, err)

	assert.Equal(t, "claude-opus-5", res.Decision.Model)
	assert.Equal(t, "medium", res.Decision.Effort)
}

// A TTL refresh rewrites the row, and the upsert always takes the incoming
// effort — so the refresh must carry the stored level forward.
func TestRefreshPin_CarriesEffortForward(t *testing.T) {
	store := &recordingPinStore{}
	svc := NewService(nil, nil, nil, false, nil, store, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil)

	existing := sessionpin.Pin{
		Provider: providers.ProviderAnthropic,
		Model:    "claude-opus-4-8",
		Effort:   "xhigh",
		Reason:   translate.ReasonUserForceModel,
	}
	svc.refreshPin(context.Background(), uuid.New(), [sessionpin.SessionKeyLen]byte{},
		existing, "default", router.Decision{
			Provider: existing.Provider, Model: existing.Model, Reason: existing.Reason,
		})
	require.Len(t, store.upserts, 1)
	assert.Equal(t, "xhigh", store.upserts[0].Effort)

	svc.refreshPin(context.Background(), uuid.New(), [sessionpin.SessionKeyLen]byte{},
		existing, "default", router.Decision{
			Provider: providers.ProviderAnthropic, Model: "claude-haiku-4-5",
		})
	require.Len(t, store.upserts, 2)
	assert.Empty(t, store.upserts[1].Effort,
		"a refresh onto a different model must not inherit the old level")
}

func TestOrderBandPair_AnchoredHalfKeepsEffort(t *testing.T) {
	large, small := orderBandPair(sessionpin.Pin{
		Provider:       providers.ProviderAnthropic,
		Model:          "claude-opus-4-7",
		Effort:         "high",
		PairedProvider: providers.ProviderAnthropic,
		PairedModel:    "claude-haiku-4-5",
	})
	assert.Equal(t, "claude-opus-4-7", large.Model)
	assert.Equal(t, "high", large.Effort)
	assert.Empty(t, small.Effort)
}

// An explicit per-request knob still outranks whatever the pin carries.
func TestResolveEffort_KnobWinsOverDecision(t *testing.T) {
	svc := NewService(nil, nil, nil, false, nil, nil, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil)
	decision := router.Decision{Provider: providers.ProviderAnthropic, Model: "claude-opus-4-8", Effort: "xhigh"}
	caps := router.Lookup(decision.Model)

	ctx := router.WithRoutingKnobs(context.Background(), &router.Overrides{ForceEffort: "low"})
	knobbed := svc.resolveEffort(ctx, decision, caps, false)
	assert.Equal(t, "low", knobbed.Selected)
	assert.Equal(t, effortSourceUser, knobbed.Source)

	arm := svc.resolveEffort(context.Background(), decision, caps, false)
	assert.Equal(t, "xhigh", arm.Selected)
	assert.Equal(t, effortSourceArm, arm.Source)

	assert.Empty(t, svc.resolveEffort(context.Background(),
		router.Decision{Provider: decision.Provider, Model: decision.Model}, caps, false).Selected)
}
