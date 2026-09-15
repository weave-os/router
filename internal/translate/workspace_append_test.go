package translate_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"weave-os/router/internal/translate"
)

const workspaceMarker = "inspect the workspace"

func TestWorkspaceAppend_AnthropicTargetIsNeverTouched(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(claudeCodeCachedSystemBody))
	require.NoError(t, err)

	off, err := env.PrepareAnthropic(nil, translate.EmitOptions{TargetModel: "claude-opus-5"})
	require.NoError(t, err)
	on, err := env.PrepareAnthropic(nil, translate.EmitOptions{TargetModel: "claude-opus-5", AppendWorkspaceSystem: true})
	require.NoError(t, err)

	assert.Equal(t, string(off.Body), string(on.Body), "an Anthropic-served attempt keeps the client prompt byte-identical")
	assert.NotContains(t, string(on.Body), workspaceMarker)
}

func TestWorkspaceAppend_OpenAIChatSystemMessage(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(claudeCodeCachedSystemBody))
	require.NoError(t, err)

	on, err := env.PrepareOpenAI(nil, translate.EmitOptions{TargetModel: "deepseek/deepseek-v4-pro", AppendWorkspaceSystem: true})
	require.NoError(t, err)
	first := gjson.GetBytes(on.Body, "messages.0")
	require.Equal(t, "system", first.Get("role").String())
	content := first.Get("content").String()
	assert.Contains(t, content, translate.WorkspaceSystemText)
	assert.Less(t, strings.Index(content, "ask for confirmation before proceeding"), strings.Index(content, workspaceMarker), "append follows the client prompt")
	assert.Equal(t, 1, strings.Count(content, workspaceMarker), "appended exactly once")
	assert.NotContains(t, content, "operating autonomously", "workspace append does not drag the autonomy text along")

	off, err := env.PrepareOpenAI(nil, translate.EmitOptions{TargetModel: "deepseek/deepseek-v4-pro"})
	require.NoError(t, err)
	assert.NotContains(t, string(off.Body), workspaceMarker)
}

func TestWorkspaceAppend_OpenAIResponsesInstructions(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(claudeCodeCachedSystemBody))
	require.NoError(t, err)

	on, err := env.PrepareOpenAIResponses(nil, translate.EmitOptions{TargetModel: "gpt-5.6-luna", AppendWorkspaceSystem: true})
	require.NoError(t, err)
	instructions := gjson.GetBytes(on.Body, "instructions").String()
	assert.Contains(t, instructions, translate.WorkspaceSystemText)
	assert.Less(t, strings.Index(instructions, "ask for confirmation before proceeding"), strings.Index(instructions, workspaceMarker), "append follows the client prompt")
	assert.Equal(t, 1, strings.Count(instructions, workspaceMarker))
	assert.NotContains(t, string(on.Body), "cache_control")

	off, err := env.PrepareOpenAIResponses(nil, translate.EmitOptions{TargetModel: "gpt-5.6-luna"})
	require.NoError(t, err)
	assert.NotContains(t, gjson.GetBytes(off.Body, "instructions").String(), workspaceMarker)
}

func TestWorkspaceAppend_BothAppendsKeepOrder(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(claudeCodeCachedSystemBody))
	require.NoError(t, err)

	out, err := env.PrepareOpenAIResponses(nil, translate.EmitOptions{TargetModel: "gpt-5.6-luna", AppendAutonomySystem: true, AppendWorkspaceSystem: true})
	require.NoError(t, err)
	instructions := gjson.GetBytes(out.Body, "instructions").String()
	client := strings.Index(instructions, "ask for confirmation before proceeding")
	autonomy := strings.Index(instructions, "operating autonomously")
	workspace := strings.Index(instructions, workspaceMarker)
	assert.Less(t, client, autonomy)
	assert.Less(t, autonomy, workspace, "workspace text is the final append")
}

func TestWorkspaceAppend_GeminiSystemInstructionCarriesTheText(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(claudeCodeCachedSystemBody))
	require.NoError(t, err)

	for _, model := range []string{"gemini-2.5-pro", "gemini-3-pro-preview"} {
		on, err := env.PrepareGemini(nil, translate.EmitOptions{TargetModel: model, AppendWorkspaceSystem: true})
		require.NoError(t, err)
		sys := gjson.GetBytes(on.Body, "systemInstruction.parts.0.text").String()
		assert.Contains(t, sys, "ask for confirmation before proceeding", "client prompt still carried for %s", model)
		assert.Contains(t, sys, translate.WorkspaceSystemText, "append reaches systemInstruction for %s", model)
		assert.Less(t, strings.Index(sys, "ask for confirmation before proceeding"), strings.Index(sys, workspaceMarker), "append follows the client prompt for %s", model)

		off, err := env.PrepareGemini(nil, translate.EmitOptions{TargetModel: model})
		require.NoError(t, err)
		assert.NotContains(t, gjson.GetBytes(off.Body, "systemInstruction.parts.0.text").String(), workspaceMarker, "flag off leaves %s untouched", model)
	}
}

func TestWorkspaceAppend_HasWorkspaceSystemText(t *testing.T) {
	plain, err := translate.ParseAnthropic([]byte(claudeCodeCachedSystemBody))
	require.NoError(t, err)
	assert.False(t, plain.HasWorkspaceSystemText())

	carried, err := translate.ParseAnthropic([]byte(`{"model":"claude-opus-5","system":[{"type":"text","text":"You are Claude Code."},{"type":"text","text":"Before answering, use your tools to Inspect The Workspace."}],"messages":[{"role":"user","content":"hi"}],"max_tokens":8}`))
	require.NoError(t, err)
	assert.True(t, carried.HasWorkspaceSystemText(), "match is case-insensitive on the marker phrase")

	autonomyOnly, err := translate.ParseAnthropic([]byte(`{"model":"claude-opus-5","system":"` + translate.AutonomySystemText + `","messages":[{"role":"user","content":"hi"}],"max_tokens":8}`))
	require.NoError(t, err)
	assert.False(t, autonomyOnly.HasWorkspaceSystemText(), "the two appends are tracked independently")
}
