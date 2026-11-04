package proxy

import (
	"context"
	"strings"
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

// An agent returning a screenshot nests the image inside a tool_result rather
// than putting it at the top level of the message content.
const toolResultImageBody = `{"model":"claude-sonnet-4-6","messages":[
	{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{"file_path":"/tmp/shot.png"}}]},
	{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[
		{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAA"}}]}]}]}`

const textOnlyForcedModel = "qwen/qwen3-coder-next"

// Regression: a user-forced pin skipped every automatic capability gate, so an
// image-bearing turn dispatched to a text-only model and the upstream rejected
// the request outright ("... is not a multimodal model").
func TestRunTurnLoop_ForcedTextOnlyModel_DropsPinForImageTurn(t *testing.T) {
	require.False(t, catalog.AcceptsImages(textOnlyForcedModel), "test premise: forced model is text-only")
	require.True(t, catalog.AcceptsImages("claude-sonnet-4-6"), "test premise: replacement accepts images")

	fr := &tierProbeRouter{available: map[string]struct{}{
		textOnlyForcedModel: {},
		"claude-sonnet-4-6": {},
	}}
	store := &forcedPinStore{pin: sessionpin.Pin{
		Provider:    providers.ProviderFireworks,
		Model:       textOnlyForcedModel,
		Reason:      translate.ReasonUserForceModel,
		PinnedUntil: time.Now().Add(time.Hour),
	}}
	svc := NewService(fr, nil, nil, false, nil, store, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil).
		WithAvailableModels(fr.available).
		WithPlannerEnabled(false)

	env, err := translate.ParseAnthropic([]byte(toolResultImageBody))
	require.NoError(t, err)
	feats := env.RoutingFeatures(false)
	require.True(t, feats.HasImages, "the nested tool_result image must register as an image-bearing turn")

	res, err := svc.runTurnLoop(context.Background(), env, feats, "key-1", uuid.New(), "", nil, router.Request{
		RequestedModel: feats.Model,
		HasImages:      feats.HasImages,
	})
	require.NoError(t, err)

	assert.NotEqual(t, textOnlyForcedModel, res.Decision.Model,
		"a text-only forced pin must not serve an image-bearing turn")
	assert.True(t, catalog.AcceptsImages(res.Decision.Model),
		"the replacement must accept images")
	assert.False(t, res.StickyHit, "the pin was dropped, so this is not a sticky hit")

	require.Len(t, fr.captured, 1, "exactly one scorer call after the pin is dropped")
	_, textOnlyExcluded := fr.captured[0].ExcludedModels[textOnlyForcedModel]
	assert.True(t, textOnlyExcluded,
		"the dropped text-only model must be excluded from the scorer pool, not left available to re-pick")
}

// Control: the same forced pin must still be honored when the turn carries no
// image, so the guard can't be mistaken for "forced pins stopped working".
func TestRunTurnLoop_ForcedTextOnlyModel_HonoredWithoutImages(t *testing.T) {
	fr := &tierProbeRouter{available: map[string]struct{}{
		textOnlyForcedModel: {},
		"claude-sonnet-4-6": {},
	}}
	store := &forcedPinStore{pin: sessionpin.Pin{
		Provider:    providers.ProviderFireworks,
		Model:       textOnlyForcedModel,
		Reason:      translate.ReasonUserForceModel,
		PinnedUntil: time.Now().Add(time.Hour),
	}}
	svc := NewService(fr, nil, nil, false, nil, store, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil).
		WithAvailableModels(fr.available).
		WithPlannerEnabled(false)

	env, err := translate.ParseAnthropic([]byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"plain text turn"}]}`))
	require.NoError(t, err)
	feats := env.RoutingFeatures(false)
	require.False(t, feats.HasImages)

	res, err := svc.runTurnLoop(context.Background(), env, feats, "key-1", uuid.New(), "", nil, router.Request{
		RequestedModel: feats.Model,
		HasImages:      feats.HasImages,
	})
	require.NoError(t, err)

	assert.Equal(t, textOnlyForcedModel, res.Decision.Model, "a text-only forced pin still serves text-only turns")
	assert.True(t, res.StickyHit)
	assert.Empty(t, fr.captured, "an honored forced pin must not invoke the scorer")
}

func TestRunTurnLoop_ForcedImageCapModel_ReachesProvider(t *testing.T) {
	const model = "xiaomi/mimo-v2.6-pro"
	const imageCount = 17
	available := map[string]struct{}{model: {}}
	fr := &tierProbeRouter{available: available}
	svc := NewService(fr, nil, nil, false, nil, &forcedPinStore{}, false,
		providers.ProviderDeepInfra, model, nil).
		WithAvailableModels(available).
		WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderDeepInfra: {}}).
		WithPlannerEnabled(false)

	const imageBlock = `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAA"}}`
	imageBlocks := strings.TrimSuffix(strings.Repeat(imageBlock+",", imageCount), ",")
	env, err := translate.ParseAnthropic([]byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":[` + imageBlocks + `]}]}`))
	require.NoError(t, err)
	feats := env.RoutingFeatures(false)
	require.Equal(t, imageCount, feats.ImageCount)

	excluded := map[string]struct{}{model: {}}
	req := router.Request{
		RequestedModel:           feats.Model,
		ForceModel:               model,
		HasImages:                true,
		ImageCount:               imageCount,
		EnabledProviders:         map[string]struct{}{providers.ProviderDeepInfra: {}},
		ExcludedModels:           excluded,
		ImageLimitExcludedModels: excluded,
		SafetyExcludedModels:     excluded,
	}
	pin := sessionpin.Pin{Provider: providers.ProviderDeepInfra, Model: model}
	assert.True(t, forcedPinEligible(pin, req), "the explicit force must survive an image-count-only exclusion")

	res, err := svc.runTurnLoop(context.Background(), env, feats, "key-1", uuid.New(), "", nil, req)
	require.NoError(t, err)
	assert.Equal(t, model, res.Decision.Model)
	assert.Equal(t, providers.ProviderDeepInfra, res.Decision.Provider)
	assert.Equal(t, translate.ReasonUserForceModel, res.Decision.Reason)
	assert.True(t, res.StickyHit)
	assert.Empty(t, fr.captured, "an honored force must not invoke the scorer")

	bindings := svc.resolveBindingsForDispatch(withRequestImageCount(context.Background(), imageCount), res.Decision)
	require.Len(t, bindings, 1)
	assert.Equal(t, providers.ProviderDeepInfra, bindings[0].Provider)
}

// forcedPinEligible is the gate for the hard-pinned-turn fast path, which is a
// separate branch from the one above and needs the same guard.
func TestForcedPinEligible_RejectsTextOnlyModelOnImageTurn(t *testing.T) {
	pin := sessionpin.Pin{Provider: providers.ProviderFireworks, Model: textOnlyForcedModel}

	assert.False(t, forcedPinEligible(pin, router.Request{HasImages: true}),
		"text-only forced pin must be ineligible for an image-bearing turn")
	assert.True(t, forcedPinEligible(pin, router.Request{HasImages: false}),
		"same pin stays eligible without images")
	assert.True(t, forcedPinEligible(
		sessionpin.Pin{Provider: providers.ProviderAnthropic, Model: "claude-sonnet-4-6"},
		router.Request{HasImages: true}),
		"an image-capable forced pin stays eligible on an image turn")
}
