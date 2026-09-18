package translate

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const workspaceCachedBody = `{
	"model":"claude-opus-5",
	"system":[
		{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.232; cc_entrypoint=sdk-cli"},
		{"type":"text","text":"You are a Claude agent, built on Anthropic's Claude Agent SDK.","cache_control":{"type":"ephemeral"}}
	],
	"messages":[{"role":"user","content":"how many TIMESTAMP_TZ columns are there?"}],
	"max_tokens":256
}`

func TestWithWorkspaceSystemAppended_UncachedBlockAfterClientBreakpoint(t *testing.T) {
	env, err := ParseAnthropic([]byte(workspaceCachedBody))
	require.NoError(t, err)

	on, err := env.withWorkspaceSystemAppended(EmitOptions{AppendWorkspaceSystem: true})
	require.NoError(t, err)

	before := gjson.GetBytes(env.body, "system").Array()
	after := gjson.GetBytes(on.body, "system").Array()
	require.Len(t, before, 2)
	require.Len(t, after, 3)
	for i := range before {
		assert.Equal(t, before[i].Raw, after[i].Raw, "cached prefix block %d must be byte-identical", i)
	}
	assert.True(t, after[1].Get("cache_control").Exists(), "client breakpoint stays on the prompt block")
	assert.Equal(t, WorkspaceSystemText, after[2].Get("text").String())
	assert.False(t, after[2].Get("cache_control").Exists(), "the append must not carry a breakpoint")
	assert.True(t, on.HasWorkspaceSystemText())
}

func TestWithWorkspaceSystemAppended_OffOrNonAnthropicSourceIsIdentity(t *testing.T) {
	env, err := ParseAnthropic([]byte(workspaceCachedBody))
	require.NoError(t, err)
	off, err := env.withWorkspaceSystemAppended(EmitOptions{})
	require.NoError(t, err)
	assert.Same(t, env, off)

	openai, err := ParseOpenAI([]byte(`{"model":"gpt-5.6-luna","messages":[{"role":"system","content":"You are Claude Code."},{"role":"user","content":"hi"}]}`))
	require.NoError(t, err)
	same, err := openai.withWorkspaceSystemAppended(EmitOptions{AppendWorkspaceSystem: true})
	require.NoError(t, err)
	assert.Same(t, openai, same, "only Claude Code's Anthropic-format requests are appended")
}
