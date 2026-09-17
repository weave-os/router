package translate_test

import (
	"net/http"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func prepareAnthropicBody(t *testing.T, body []byte) []byte {
	t.Helper()
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	prep, err := env.PrepareAnthropic(http.Header{}, translate.EmitOptions{TargetModel: "claude-opus-4-8"})
	require.NoError(t, err)
	return prep.Body
}

func prepareAnthropicPassthroughBody(t *testing.T, body []byte) []byte {
	t.Helper()
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	prep, err := env.PrepareAnthropicPassthrough(http.Header{})
	require.NoError(t, err)
	return prep.Body
}

func TestPrepareAnthropic_NormalizesToolAdditionOnNonSystemMessage(t *testing.T) {
	body := []byte(`{
		"model": "claude-opus-4-8",
		"max_tokens": 1024,
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "hello"}]},
			{"role": "user", "content": [
				{"type": "tool_addition", "toolset_id": "skills"},
				{"type": "text", "text": "# Environment"}
			]}
		]
	}`)
	out := prepareAnthropicBody(t, body)

	msgs := gjson.GetBytes(out, "messages").Array()
	require.Len(t, msgs, 3)
	second := msgs[1].Get("content")
	require.True(t, second.IsArray())
	require.Equal(t, 1, int(second.Get("#").Int()))
	assert.Equal(t, "text", second.Get("0.type").String())
	assert.Equal(t, "# Environment", second.Get("0.text").String())
	system := msgs[2]
	assert.Equal(t, "system", system.Get("role").String())
	assert.Equal(t, "tool_addition", system.Get("content.0.type").String())
}

func TestPrepareAnthropic_PreservesSystemToolDeltasAfterSystemHandling(t *testing.T) {
	body := []byte(`{
		"model": "claude-opus-4-8",
		"max_tokens": 1024,
		"messages": [
			{"role": "user", "content": "hello"},
			{"role": "system", "content": [
				{"type": "tool_removal", "toolset_id": "skills"},
				{"type": "text", "text": "# Environment"}
			]}
		]
	}`)
	out := prepareAnthropicBody(t, body)

	for _, msg := range gjson.GetBytes(out, "messages").Array() {
		if msg.Get("role").String() == "system" {
			continue
		}
		content := msg.Get("content")
		if !content.IsArray() {
			continue
		}
		for _, part := range content.Array() {
			assert.NotEqual(t, "tool_removal", part.Get("type").String(), msg.Raw)
			assert.NotEqual(t, "tool_addition", part.Get("type").String(), msg.Raw)
		}
	}
	msgs := gjson.GetBytes(out, "messages").Array()
	require.Len(t, msgs, 2)
	assert.Equal(t, "system", msgs[1].Get("role").String())
	assert.Equal(t, "tool_removal", msgs[1].Get("content.0.type").String())
}

func TestPrepareAnthropic_DropsMessageThatOnlyHadSystemOnlyBlocks(t *testing.T) {
	body := []byte(`{
		"model": "claude-opus-4-8",
		"max_tokens": 1024,
		"messages": [
			{"role": "user", "content": "hello"},
			{"role": "user", "content": [{"type": "tool_addition", "toolset_id": "skills"}]}
		]
	}`)
	out := prepareAnthropicBody(t, body)
	msgs := gjson.GetBytes(out, "messages").Array()
	require.Len(t, msgs, 2)
	assert.Equal(t, "user", msgs[0].Get("role").String())
	assert.Equal(t, "system", msgs[1].Get("role").String())
	assert.Equal(t, "tool_addition", msgs[1].Get("content.0.type").String())
}

func TestPrepareAnthropic_PlacesAssistantToolDeltaBeforeAssistantTurn(t *testing.T) {
	body := []byte(`{
		"model": "claude-opus-4-8",
		"max_tokens": 1024,
		"messages": [
			{"role": "user", "content": "start"},
			{"role": "assistant", "content": [
				{"type": "tool_addition", "toolset_id": "skills"},
				{"type": "tool_use", "id": "call_1", "name": "Read", "input": {}}
			]},
			{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "call_1", "content": "ok"}]}
		]
	}`)
	out := prepareAnthropicBody(t, body)

	msgs := gjson.GetBytes(out, "messages").Array()
	require.Len(t, msgs, 4)
	assert.Equal(t, "user", msgs[0].Get("role").String())
	assert.Equal(t, "system", msgs[1].Get("role").String())
	assert.Equal(t, "tool_addition", msgs[1].Get("content.0.type").String())
	assert.Equal(t, "assistant", msgs[2].Get("role").String())
	assert.Equal(t, "tool_use", msgs[2].Get("content.0.type").String())
	assert.Equal(t, "user", msgs[3].Get("role").String())
	assert.Equal(t, "tool_result", msgs[3].Get("content.0.type").String())
}

func TestPrepareAnthropicPassthrough_NormalizesToolDeltas(t *testing.T) {
	body := []byte(`{
		"model": "claude-opus-4-8",
		"max_tokens": 1024,
		"messages": [{"role": "user", "content": [
			{"type": "tool_removal", "tool": {"type": "tool_reference", "name": "search"}},
			{"type": "text", "text": "continue"}
		]}]
	}`)
	out := prepareAnthropicPassthroughBody(t, body)

	msgs := gjson.GetBytes(out, "messages").Array()
	require.Len(t, msgs, 2)
	assert.Equal(t, "user", msgs[0].Get("role").String())
	assert.Equal(t, "continue", msgs[0].Get("content.0.text").String())
	assert.Equal(t, "system", msgs[1].Get("role").String())
	assert.Equal(t, "tool_removal", msgs[1].Get("content.0.type").String())
}

func TestPrepareAnthropic_AddsToolChangesBeta(t *testing.T) {
	body := []byte(`{
		"model": "claude-opus-5",
		"max_tokens": 1024,
		"messages": [{"role": "user", "content": [
			{"type": "tool_addition", "tool": {"type": "tool_reference", "name": "Read"}},
			{"type": "text", "text": "continue"}
		]}]
	}`)
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	prep, err := env.PrepareAnthropic(http.Header{}, translate.EmitOptions{
		TargetModel:    "claude-opus-5",
		TargetProvider: providers.ProviderAnthropic,
	})
	require.NoError(t, err)
	assert.Equal(t, "mid-conversation-tool-changes-2026-07-01", prep.Headers.Get("anthropic-beta"))
}

func TestPrepareAnthropicPassthrough_AddsToolChangesBeta(t *testing.T) {
	body := []byte(`{
		"model": "claude-opus-5",
		"max_tokens": 1024,
		"messages": [{"role": "user", "content": [
			{"type": "tool_removal", "tool": {"type": "tool_reference", "name": "Edit"}},
			{"type": "text", "text": "continue"}
		]}]
	}`)
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	prep, err := env.PrepareAnthropicPassthrough(http.Header{})
	require.NoError(t, err)
	assert.Equal(t, "mid-conversation-tool-changes-2026-07-01", prep.Headers.Get("anthropic-beta"))
}

func TestPrepareAnthropic_LeavesOrdinaryMessagesUnchanged(t *testing.T) {
	body := []byte(`{
		"model": "claude-opus-4-8",
		"max_tokens": 1024,
		"system": "rules",
		"messages": [{"role": "user", "content": "hello"}]
	}`)
	out := prepareAnthropicBody(t, body)
	msgs := gjson.GetBytes(out, "messages").Array()
	require.Len(t, msgs, 1)
	assert.Contains(t, msgs[0].Get("content").Raw, "hello")
	for _, msg := range msgs {
		content := msg.Get("content")
		if !content.IsArray() {
			continue
		}
		for _, part := range content.Array() {
			assert.NotEqual(t, "tool_addition", part.Get("type").String())
			assert.NotEqual(t, "tool_removal", part.Get("type").String())
		}
	}
}
