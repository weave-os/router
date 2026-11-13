package translate_test

import (
	"testing"

	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestApplyOpenAIResponsesEffort(t *testing.T) {
	const body = `{"model":"gpt-5.6-luna","input":"hi","reasoning":{"effort":"medium","summary":"auto"},"store":false}`

	for _, tc := range []struct {
		name       string
		model      string
		force      string
		wantEffort string
	}{
		{
			name:       "nothing forced keeps the caller's level",
			model:      "gpt-5.6-luna",
			wantEffort: "medium",
		},
		{
			name:       "router level replaces the caller's level",
			model:      "gpt-5.6-luna",
			force:      "low",
			wantEffort: "low",
		},
		{
			name:       "level above the target's menu clamps down",
			model:      "gpt-5.5",
			force:      "xhigh",
			wantEffort: "high",
		},
		{
			name:       "gpt-5.6 keeps xhigh",
			model:      "gpt-5.6-luna",
			force:      "xhigh",
			wantEffort: "xhigh",
		},
		{
			name:       "target the menu accepts xhigh on keeps it",
			model:      "claude-opus-5",
			force:      "xhigh",
			wantEffort: "xhigh",
		},
		{
			name:       "non-reasoning target is left alone",
			model:      "gpt-4.1",
			force:      "high",
			wantEffort: "medium",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			caps := router.Lookup(tc.model)
			out, err := translate.ApplyOpenAIResponsesEffort([]byte(body), translate.EmitOptions{
				TargetModel:          tc.model,
				Capabilities:         caps,
				ForceEffort:          tc.force,
				ForceReasoningEffort: translate.ResolveForceEffort(caps, tc.force),
			})
			require.NoError(t, err)

			assert.Equal(t, tc.wantEffort, gjson.GetBytes(out, "reasoning.effort").Str)
			assert.Equal(t, "auto", gjson.GetBytes(out, "reasoning.summary").Str,
				"unrelated native fields survive the rewrite")
			assert.Equal(t, "hi", gjson.GetBytes(out, "input").Str)
			assert.False(t, gjson.GetBytes(out, "store").Bool())
		})
	}
}
