package translate_test

import (
	"net/http"
	"testing"

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

func TestPrepareAnthropic_HoistsToolAdditionOffNonSystemMessage(t *testing.T) {
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
	require.Len(t, msgs, 2)
	second := msgs[1].Get("content")
	require.True(t, second.IsArray())
	require.Equal(t, 1, int(second.Get("#").Int()))
	assert.Equal(t, "text", second.Get("0.type").String())
	assert.Equal(t, "# Environment", second.Get("0.text").String())

	system := gjson.GetBytes(out, "system")
	require.True(t, system.IsArray(), string(out))
	var types []string
	for _, block := range system.Array() {
		types = append(types, block.Get("type").String())
	}
	assert.Contains(t, types, "tool_addition")
}

func TestPrepareAnthropic_HoistsToolDeltasAfterSystemDemotion(t *testing.T) {
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
		content := msg.Get("content")
		if !content.IsArray() {
			continue
		}
		for _, part := range content.Array() {
			assert.NotEqual(t, "tool_removal", part.Get("type").String(), msg.Raw)
			assert.NotEqual(t, "tool_addition", part.Get("type").String(), msg.Raw)
		}
	}
	var types []string
	for _, block := range gjson.GetBytes(out, "system").Array() {
		types = append(types, block.Get("type").String())
	}
	assert.Contains(t, types, "tool_removal")
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
	require.Len(t, msgs, 1)
	assert.Equal(t, "user", msgs[0].Get("role").String())
	assert.Contains(t, gjson.GetBytes(out, "system").Raw, "tool_addition")
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
