package proxy

import (
	"context"
	"testing"
	"time"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The pin's context fit-check must not lift a gemini-3.x exclusion that came
// from unsigned tool-call history: Gemini rejects that history mid-stream.
func TestTurnLoop_GeminiPinNotServedOnUnsignedToolHistory(t *testing.T) {
	const geminiPin = "gemini-3-flash-preview"
	fr := &tierProbeRouter{available: map[string]struct{}{
		"claude-haiku-4-5": {},
		geminiPin:          {},
	}}
	store := &overwritingPinStore{pin: sessionpin.Pin{
		Provider:    providers.ProviderGoogle,
		Model:       geminiPin,
		Reason:      "cluster:v0.2",
		PinnedUntil: time.Now().Add(time.Hour),
	}, found: true}
	svc := NewService(fr, nil, nil, false, nil, store, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil)

	env, err := translate.ParseAnthropic([]byte(`{"model":"claude-opus-4-8","messages":[
		{"role":"user","content":"run it"},
		{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"}]}
	]}`))
	require.NoError(t, err)
	require.True(t, env.HasUnsignedToolCallHistory())
	feats := env.RoutingFeatures(false)

	res, err := svc.runTurnLoop(context.Background(),
		env, feats, "key-1", uuid.New(), "", nil,
		router.Request{
			RequestedModel:                feats.Model,
			ExcludedModels:                map[string]struct{}{geminiPin: {}},
			SafetyExcludedModels:          map[string]struct{}{geminiPin: {}},
			UnsignedHistoryExcludedModels: map[string]struct{}{geminiPin: {}},
		})

	require.NoError(t, err)
	assert.NotEqual(t, geminiPin, res.Decision.Model,
		"a gemini-3.x pin must not be served over unsigned tool-call history")
	require.NotEmpty(t, fr.captured, "the scorer must run once the pin is dropped")
	assert.Contains(t, fr.captured[len(fr.captured)-1].ExcludedModels, geminiPin)
}
