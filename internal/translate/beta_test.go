package translate_test

import (
	"testing"

	"workweave/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestExtractBetaCommand(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		found       bool
		invalid     bool
		wantContent string
	}{
		{name: "toggle", input: "/beta", found: true},
		{name: "whitespace", input: "  /beta  ", found: true},
		{name: "arguments rejected", input: "/beta status", found: true, invalid: true},
		{name: "trailing prompt rejected", input: "/beta\nroute this", found: true, invalid: true, wantContent: "route this"},
		{name: "non-leading ignored", input: "route this\n/beta", found: false, wantContent: "route this\n/beta"},
		{name: "prefix boundary", input: "/betamax", found: false, wantContent: "/betamax"},
		{
			name:        "injected prefix",
			input:       "<command-name>/beta</command-name>\n/beta",
			found:       true,
			wantContent: "<command-name>/beta</command-name>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, err := translate.ParseAnthropic(buildAnthropicBody(t, tt.input))
			require.NoError(t, err)

			result, found := env.ExtractBetaCommand()
			assert.Equal(t, tt.found, found)
			assert.Equal(t, tt.invalid, result.Invalid)
			assert.Equal(t, tt.wantContent, lastUserMessageText(t, env))
		})
	}
}

func TestExtractBetaCommandOpenAI(t *testing.T) {
	body := mustMarshalJSON(t, map[string]any{
		"model": "gpt-5.6-sol",
		"messages": []any{
			map[string]any{"role": "user", "content": "/beta"},
		},
	})
	env, err := translate.ParseOpenAI(body)
	require.NoError(t, err)

	result, found := env.ExtractBetaCommand()
	require.True(t, found)
	assert.False(t, result.Invalid)
	assert.Empty(t, lastOpenAIUserMessageText(t, env))
}

func TestStripBetaArtifactsAnthropicPreservesCurrentToggle(t *testing.T) {
	body := mustMarshalJSON(t, map[string]any{
		"model": "claude-sonnet-5",
		"messages": []any{
			map[string]any{"role": "user", "content": "inspect this repository"},
			map[string]any{"role": "assistant", "content": "I will inspect it."},
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "text", "text": "<command-message>beta</command-message>\n<command-name>/beta</command-name>"},
					map[string]any{"type": "text", "text": "/beta"},
				},
			},
			map[string]any{
				"role": "assistant",
				"content": []any{
					map[string]any{"type": "text", "text": "✦ **Weave Router** → Beta enabled. Type /beta again to turn it off.\n\n"},
				},
			},
			map[string]any{"role": "user", "content": "/beta"},
		},
		"max_tokens": 128,
	})
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)

	assert.Equal(t, 2, env.StripBetaArtifacts())
	prepared, err := env.PrepareAnthropic(nil, translate.EmitOptions{TargetModel: "claude-sonnet-5"})
	require.NoError(t, err)
	assert.Equal(t, int64(3), gjson.GetBytes(prepared.Body, "messages.#").Int())
	assert.Equal(t, "/beta", gjson.GetBytes(prepared.Body, "messages.2.content.0.text").String(),
		"the current trailing toggle must survive until interception")

	result, found := env.ExtractBetaCommand()
	require.True(t, found)
	assert.False(t, result.Invalid)
}

func TestStripBetaArtifactsOpenAI(t *testing.T) {
	body := mustMarshalJSON(t, map[string]any{
		"model": "gpt-5.6-sol",
		"messages": []any{
			map[string]any{"role": "user", "content": "inspect this repository"},
			map[string]any{"role": "assistant", "content": "I will inspect it."},
			map[string]any{"role": "user", "content": "/beta"},
			map[string]any{"role": "assistant", "content": "✦ **Weave Router** → Beta is unavailable for this session.\n\n"},
			map[string]any{"role": "user", "content": "continue with the implementation"},
		},
	})
	env, err := translate.ParseOpenAI(body)
	require.NoError(t, err)

	assert.Equal(t, 2, env.StripBetaArtifacts())
	prepared, err := env.PrepareOpenAI(nil, translate.EmitOptions{TargetModel: "gpt-5.6-sol"})
	require.NoError(t, err)
	assert.Equal(t, int64(3), gjson.GetBytes(prepared.Body, "messages.#").Int())
	assert.Equal(t, "continue with the implementation", gjson.GetBytes(prepared.Body, "messages.2.content").String())
}

func TestStripBetaArtifactsRemovesInvalidControlTurnButKeepsDiscussion(t *testing.T) {
	body := mustMarshalJSON(t, map[string]any{
		"model": "claude-sonnet-5",
		"messages": []any{
			map[string]any{"role": "user", "content": "/beta status"},
			map[string]any{"role": "assistant", "content": "✦ **Weave Router** → Usage: /beta\n\n"},
			map[string]any{"role": "user", "content": "Please explain /beta instead of toggling it."},
			map[string]any{"role": "assistant", "content": "✦ **Weave Router** → Beta enabled. Type /beta again to turn it off. This is quoted documentation."},
			map[string]any{"role": "user", "content": "continue"},
		},
		"max_tokens": 128,
	})
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)

	assert.Equal(t, 2, env.StripBetaArtifacts())
	prepared, err := env.PrepareAnthropic(nil, translate.EmitOptions{TargetModel: "claude-sonnet-5"})
	require.NoError(t, err)
	assert.Equal(t, int64(3), gjson.GetBytes(prepared.Body, "messages.#").Int())
	assert.Equal(t, "Please explain /beta instead of toggling it.", gjson.GetBytes(prepared.Body, "messages.0.content").String())
	assert.Contains(t, gjson.GetBytes(prepared.Body, "messages.1.content").String(), "quoted documentation")
}
