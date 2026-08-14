package proxy

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"workweave/router/internal/providers"
	"workweave/router/internal/router"
	"workweave/router/internal/router/sessionpin"
	"workweave/router/internal/router/turntype"
	"workweave/router/internal/translate"
)

func TestWithRespectRequestedModelCanonicalizes(t *testing.T) {
	t.Parallel()

	s := (&Service{}).WithRespectRequestedModel([]string{" Haiku ", "opus-4.8"})

	require.NotNil(t, s.respectRequestedModel)
	assert.Contains(t, s.respectRequestedModel, "claude-opus-4-8", "alias resolves to canonical id")
	assert.Len(t, s.respectRequestedModel, 2)
}

func TestWithRespectRequestedModelDropsUnknown(t *testing.T) {
	t.Parallel()

	assert.Nil(t, (&Service{}).WithRespectRequestedModel([]string{"totally-not-a-model"}).respectRequestedModel)
	assert.Nil(t, (&Service{}).WithRespectRequestedModel(nil).respectRequestedModel)
}

func TestHonoredRequestedModel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		honored      []string
		req          router.Request
		wantOK       bool
		wantModel    string
		wantProvider string
	}{
		{
			name:         "listed model is served verbatim",
			honored:      []string{"haiku"},
			req:          router.Request{RequestedModel: "claude-haiku-4-5"},
			wantOK:       true,
			wantModel:    "claude-haiku-4-5",
			wantProvider: providers.ProviderAnthropic,
		},
		{
			name:    "unlisted model still routes automatically",
			honored: []string{"haiku"},
			req:     router.Request{RequestedModel: "claude-opus-5"},
			wantOK:  false,
		},
		{
			name:    "feature off honors nothing",
			honored: nil,
			req:     router.Request{RequestedModel: "claude-haiku-4-5"},
			wantOK:  false,
		},
		{
			name:    "no requested model",
			honored: []string{"haiku"},
			req:     router.Request{RequestedModel: ""},
			wantOK:  false,
		},
		{
			name:    "unknown requested model",
			honored: []string{"haiku"},
			req:     router.Request{RequestedModel: "totally-not-a-model"},
			wantOK:  false,
		},
		{
			name:    "excluded model falls back to routing",
			honored: []string{"haiku"},
			req: router.Request{
				RequestedModel: "claude-haiku-4-5",
				ExcludedModels: map[string]struct{}{"claude-haiku-4-5": {}},
			},
			wantOK: false,
		},
		{
			name:    "ineligible provider falls back to routing",
			honored: []string{"haiku"},
			req: router.Request{
				RequestedModel:   "claude-haiku-4-5",
				EnabledProviders: map[string]struct{}{providers.ProviderOpenAI: {}},
			},
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := (&Service{}).WithRespectRequestedModel(tt.honored)
			model, provider, ok := s.honoredRequestedModel(context.Background(), tt.req)

			assert.Equal(t, tt.wantOK, ok, "honored")
			assert.Equal(t, tt.wantModel, model, "model")
			assert.Equal(t, tt.wantProvider, provider, "provider")
		})
	}
}

func TestTurnLoop_UserForceBeatsHonoredRequestedModel(t *testing.T) {
	store := &overwritingPinStore{pin: sessionpin.Pin{
		Provider:    providers.ProviderAnthropic,
		Model:       "claude-opus-5",
		Reason:      translate.ReasonUserForceModel,
		PinnedUntil: pinNeverExpires,
	}, found: true}
	svc := (&Service{}).WithRespectRequestedModel([]string{"haiku"})
	svc.pinStore = store

	env, err := translate.ParseAnthropic([]byte(
		`{"model":"claude-haiku-4-5","messages":[{"role":"user","content":"hi"}]}`))
	require.NoError(t, err)
	feats := env.RoutingFeatures(false)
	res, err := svc.runTurnLoop(context.Background(), env, feats, "key-1", uuid.New(), "", nil,
		router.Request{RequestedModel: feats.Model})

	require.NoError(t, err)
	assert.Equal(t, "claude-opus-5", res.Decision.Model)
	assert.Equal(t, providers.ProviderAnthropic, res.Decision.Provider)
	assert.True(t, res.StickyHit)
}

func TestApplyToolResultTierCeiling(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		enabled        bool
		tt             turntype.TurnType
		requestedModel string
		wantExcludes   []string // asserted present in the result
		wantKeeps      []string // asserted absent from the result
	}{
		{
			name:           "disabled is a no-op even on a ToolResult turn",
			enabled:        false,
			tt:             turntype.ToolResult,
			requestedModel: "claude-haiku-4-5",
			wantKeeps:      []string{"claude-opus-5", "claude-sonnet-5"},
		},
		{
			name:           "enabled but not ToolResult is a no-op",
			enabled:        true,
			tt:             turntype.MainLoop,
			requestedModel: "claude-haiku-4-5",
			wantKeeps:      []string{"claude-opus-5", "claude-sonnet-5"},
		},
		{
			name:           "unknown requested-model tier is a no-op",
			enabled:        true,
			tt:             turntype.ToolResult,
			requestedModel: "totally-not-a-model",
			wantKeeps:      []string{"claude-opus-5", "claude-sonnet-5"},
		},
		{
			name:           "haiku ceiling excludes mid and high tier, keeps haiku",
			enabled:        true,
			tt:             turntype.ToolResult,
			requestedModel: "claude-haiku-4-5",
			wantExcludes:   []string{"claude-sonnet-5", "claude-opus-5", "claude-fable-5"},
			wantKeeps:      []string{"claude-haiku-4-5"},
		},
		{
			name:           "sonnet ceiling excludes only high tier, keeps sonnet and haiku",
			enabled:        true,
			tt:             turntype.ToolResult,
			requestedModel: "claude-sonnet-5",
			wantExcludes:   []string{"claude-opus-5", "claude-fable-5"},
			wantKeeps:      []string{"claude-sonnet-5", "claude-haiku-4-5"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := &Service{toolResultTierCeiling: tt.enabled}
			got := s.applyToolResultTierCeiling(map[string]struct{}{}, tt.tt, tt.requestedModel)

			for _, m := range tt.wantExcludes {
				assert.Contains(t, got, m, "expected %s excluded", m)
			}
			for _, m := range tt.wantKeeps {
				assert.NotContains(t, got, m, "expected %s not excluded", m)
			}
		})
	}
}

func TestApplyToolResultTierCeiling_NoSurvivorIsNoOp(t *testing.T) {
	t.Parallel()

	s := &Service{
		toolResultTierCeiling: true,
		availableModels:       map[string]struct{}{"claude-opus-5": {}},
	}
	excluded := map[string]struct{}{"already-excluded": {}}
	got := s.applyToolResultTierCeiling(excluded, turntype.ToolResult, "claude-haiku-4-5")

	assert.Equal(t, excluded, got)
}

func TestRunTurnLoop_ToolResultCeilingAppliesAcrossSurfaces(t *testing.T) {
	tests := []struct {
		name string
		env  *translate.RequestEnvelope
	}{
		{
			name: "messages",
			env:  mustParseEnvelope(t, `{"model":"claude-haiku-4-5","messages":[{"role":"user","content":"run"},{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"done"}]}]}`, true),
		},
		{
			name: "openai_chat_completions",
			env:  mustParseEnvelope(t, `{"model":"gpt-5.4-mini","messages":[{"role":"user","content":"run"},{"role":"assistant","tool_calls":[{"id":"t1","type":"function","function":{"name":"Bash","arguments":"{}"}}]},{"role":"tool","tool_call_id":"t1","content":"done"}]}`, false),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fr := &tierProbeRouter{available: map[string]struct{}{
				"claude-haiku-4-5": {},
				"claude-opus-5":    {},
				"gpt-5.4-mini":     {},
				"gpt-5.4-pro":      {},
			}}
			svc := &Service{router: fr, toolResultTierCeiling: true}
			feats := tt.env.RoutingFeatures(false)
			_, err := svc.runTurnLoop(context.Background(), tt.env, feats, "key-1", uuid.Nil, "", nil,
				router.Request{RequestedModel: feats.Model})
			require.NoError(t, err)
			require.Len(t, fr.captured, 1)
			assert.Contains(t, fr.captured[0].ExcludedModels, "gpt-5.4-pro")
		})
	}
}

func TestRunTurnLoop_ToolResultCeilingDropsAboveCeilingStickyPin(t *testing.T) {
	env := mustParseEnvelope(t, `{"model":"claude-haiku-4-5","messages":[{"role":"user","content":"run"},{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"done"}]}]}`, true)
	store := &overwritingPinStore{pin: sessionpin.Pin{
		Provider:    providers.ProviderAnthropic,
		Model:       "claude-opus-5",
		Reason:      "cluster:v-test",
		PinnedUntil: pinNeverExpires,
	}, found: true}
	fr := &tierProbeRouter{available: map[string]struct{}{
		"claude-haiku-4-5": {},
		"claude-opus-5":    {},
	}}
	svc := &Service{
		router:                fr,
		pinStore:              store,
		scoreToolResultTurns:  false,
		toolResultTierCeiling: true,
	}
	feats := env.RoutingFeatures(false)
	res, err := svc.runTurnLoop(context.Background(), env, feats, "key-1", uuid.Nil, "", nil,
		router.Request{RequestedModel: feats.Model})

	require.NoError(t, err)
	assert.Equal(t, "claude-haiku-4-5", res.Decision.Model)
	assert.False(t, res.StickyHit)
}

func mustParseEnvelope(t *testing.T, body string, anthropic bool) *translate.RequestEnvelope {
	t.Helper()
	var (
		env *translate.RequestEnvelope
		err error
	)
	if anthropic {
		env, err = translate.ParseAnthropic([]byte(body))
	} else {
		env, err = translate.ParseOpenAI([]byte(body))
	}
	require.NoError(t, err)
	return env
}

func TestApplyToolResultTierCeiling_PreservesExistingExclusions(t *testing.T) {
	t.Parallel()

	s := &Service{toolResultTierCeiling: true}
	pre := map[string]struct{}{"claude-haiku-4-5": {}}
	got := s.applyToolResultTierCeiling(pre, turntype.ToolResult, "claude-sonnet-5")

	assert.Contains(t, got, "claude-haiku-4-5", "pre-existing exclusion must survive")
	assert.Contains(t, got, "claude-opus-5", "above-ceiling model must be excluded")
}
