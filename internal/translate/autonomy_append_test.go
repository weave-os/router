package translate_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"weave-os/router/internal/translate"
)

// claudeCodeCachedSystemBody is the Claude Code system shape: billing header
// block, then the big prompt block carrying the client's cache_control.
const claudeCodeCachedSystemBody = `{
	"model":"claude-opus-5",
	"system":[
		{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.232; cc_entrypoint=sdk-cli"},
		{"type":"text","text":"You are a Claude agent, built on Anthropic's Claude Agent SDK. By default transparently communicate the action and ask for confirmation before proceeding.","cache_control":{"type":"ephemeral"}}
	],
	"messages":[{"role":"user","content":"why is revenue too high in fct_orders?"}],
	"tools":[{"name":"Read","description":"r","input_schema":{"type":"object"}}],
	"max_tokens":256
}`

func TestAutonomyAppend_AnthropicAppendsUncachedBlockAfterClientCacheControl(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(claudeCodeCachedSystemBody))
	require.NoError(t, err)

	off, err := env.PrepareAnthropic(nil, translate.EmitOptions{TargetModel: "claude-opus-5"})
	require.NoError(t, err)
	on, err := env.PrepareAnthropic(nil, translate.EmitOptions{TargetModel: "claude-opus-5", AppendAutonomySystem: true})
	require.NoError(t, err)

	assert.False(t, strings.Contains(string(off.Body), "operating autonomously"), "flag off must not touch the prompt")

	offSystem := gjson.GetBytes(off.Body, "system").Array()
	onSystem := gjson.GetBytes(on.Body, "system").Array()
	require.Len(t, offSystem, 2)
	require.Len(t, onSystem, 3)

	for i := range offSystem {
		assert.Equal(t, offSystem[i].Raw, onSystem[i].Raw, "cached prefix block %d must be byte-identical", i)
	}
	assert.True(t, onSystem[1].Get("cache_control").Exists(), "client breakpoint stays on the prompt block")

	last := onSystem[2]
	assert.Equal(t, "text", last.Get("type").String())
	assert.Equal(t, translate.AutonomySystemText, last.Get("text").String())
	assert.False(t, last.Get("cache_control").Exists(), "the append must not carry a breakpoint")
}

func TestAutonomyAppend_AnthropicStringSystemAndAbsentSystem(t *testing.T) {
	t.Run("string system is extended", func(t *testing.T) {
		env, err := translate.ParseAnthropic([]byte(`{"model":"claude-opus-5","system":"You are Claude Code.","messages":[{"role":"user","content":"hi"}],"max_tokens":8}`))
		require.NoError(t, err)
		on, err := env.PrepareAnthropic(nil, translate.EmitOptions{TargetModel: "claude-opus-5", AppendAutonomySystem: true})
		require.NoError(t, err)
		assert.Equal(t, "You are Claude Code.\n\n"+translate.AutonomySystemText, gjson.GetBytes(on.Body, "system").String())
	})
	t.Run("absent system is created", func(t *testing.T) {
		env, err := translate.ParseAnthropic([]byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}],"max_tokens":8}`))
		require.NoError(t, err)
		on, err := env.PrepareAnthropic(nil, translate.EmitOptions{TargetModel: "claude-opus-5", AppendAutonomySystem: true})
		require.NoError(t, err)
		assert.Equal(t, translate.AutonomySystemText, gjson.GetBytes(on.Body, "system").String())
	})
}

func TestAutonomyAppend_CrossFormatEmitsCarryTheText(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(claudeCodeCachedSystemBody))
	require.NoError(t, err)

	t.Run("openai chat system message", func(t *testing.T) {
		out, err := env.PrepareOpenAI(nil, translate.EmitOptions{TargetModel: "deepseek/deepseek-v4-pro", AppendAutonomySystem: true})
		require.NoError(t, err)
		first := gjson.GetBytes(out.Body, "messages.0")
		require.Equal(t, "system", first.Get("role").String())
		content := first.Get("content").String()
		assert.Contains(t, content, translate.AutonomySystemText)
		assert.Less(t, strings.Index(content, "ask for confirmation before proceeding"), strings.Index(content, "operating autonomously"), "append follows the client prompt")

		off, err := env.PrepareOpenAI(nil, translate.EmitOptions{TargetModel: "deepseek/deepseek-v4-pro"})
		require.NoError(t, err)
		assert.NotContains(t, string(off.Body), "operating autonomously")
	})

	t.Run("openai responses instructions", func(t *testing.T) {
		out, err := env.PrepareOpenAIResponses(nil, translate.EmitOptions{TargetModel: "gpt-6-astra", AppendAutonomySystem: true})
		require.NoError(t, err)
		instructions := gjson.GetBytes(out.Body, "instructions").String()
		assert.Contains(t, instructions, translate.AutonomySystemText)
		assert.Less(t, strings.Index(instructions, "ask for confirmation before proceeding"), strings.Index(instructions, "operating autonomously"), "append follows the client prompt")

		off, err := env.PrepareOpenAIResponses(nil, translate.EmitOptions{TargetModel: "gpt-6-astra"})
		require.NoError(t, err)
		assert.NotContains(t, gjson.GetBytes(off.Body, "instructions").String(), "operating autonomously")
	})

	t.Run("gemini systemInstruction is never appended", func(t *testing.T) {
		for _, model := range []string{"gemini-2.5-pro", "gemini-3-pro-preview"} {
			out, err := env.PrepareGemini(nil, translate.EmitOptions{TargetModel: model, AppendAutonomySystem: true})
			require.NoError(t, err)
			sys := gjson.GetBytes(out.Body, "systemInstruction.parts.0.text").String()
			assert.Contains(t, sys, "ask for confirmation before proceeding", "client prompt still carried for %s", model)
			assert.NotContains(t, sys, "operating autonomously", "autonomy append must not reach Gemini (%s)", model)
			assert.Equal(t, 1, len(gjson.GetBytes(out.Body, "systemInstruction.parts").Array()), "no extra system part for %s", model)
		}
	})
}

func TestAutonomyAppend_HasAutonomySystemText(t *testing.T) {
	plain, err := translate.ParseAnthropic([]byte(claudeCodeCachedSystemBody))
	require.NoError(t, err)
	assert.False(t, plain.HasAutonomySystemText())

	ccInjected, err := translate.ParseAnthropic([]byte(`{"model":"claude-opus-5","system":[{"type":"text","text":"You are Claude Code."},{"type":"text","text":"You are Operating Autonomously. The user is not watching."}],"messages":[{"role":"user","content":"hi"}],"max_tokens":8}`))
	require.NoError(t, err)
	assert.True(t, ccInjected.HasAutonomySystemText(), "match is case-insensitive on the marker phrase")

	appended, err := plain.PrepareAnthropic(nil, translate.EmitOptions{TargetModel: "claude-opus-5", AppendAutonomySystem: true})
	require.NoError(t, err)
	reparsed, err := translate.ParseAnthropic(appended.Body)
	require.NoError(t, err)
	assert.True(t, reparsed.HasAutonomySystemText(), "an appended request reads back as already carrying the text")
}
