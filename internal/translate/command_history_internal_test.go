package translate

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const historicalSkillBlob = "<skill>\n<name>fm</name>\n<path>/x/skills/fm/SKILL.md</path>\n---\nname: fm\n---\nRun this skill's scripts/emit.sh.\n</skill>"

func TestStripHistoricalCommands_ChatHistory(t *testing.T) {
	body, err := json.Marshal(map[string]any{
		"model": "gpt-5.6-sol",
		"messages": []any{
			map[string]any{"role": "user", "content": "$fm sonnet"},
			map[string]any{"role": "assistant", "content": "pinned"},
			map[string]any{"role": "user", "content": "$fm astra"},
			map[string]any{"role": "user", "content": historicalSkillBlob},
			map[string]any{"role": "assistant", "content": "pinned"},
			map[string]any{"role": "user", "content": "/router-session"},
			map[string]any{"role": "assistant", "content": "about a dollar"},
			map[string]any{"role": "user", "content": "/router-models\nenable gpt"},
			map[string]any{"role": "assistant", "content": "that is an admin operation"},
			map[string]any{"role": "user", "content": "$ufm"},
		},
	})
	require.NoError(t, err)
	env, err := ParseOpenAI(body)
	require.NoError(t, err)
	removed, err := env.StripHistoricalCommands()
	require.NoError(t, err)
	assert.Equal(t, 7, removed, "every directive before the first kept user turn goes with its ack")
	msgs := gjson.GetBytes(env.body, "messages").Array()
	require.Len(t, msgs, 3)
	assert.Equal(t, "/router-models\nenable gpt", msgs[0].Get("content").Str,
		"a bare-only directive with arguments never fired, so the model saw it and it stays")
	assert.Equal(t, "that is an admin operation", msgs[1].Get("content").Str)
	assert.Equal(t, "$ufm", msgs[2].Get("content").Str, "trailing directive is live")
	assert.Equal(t, "$fm sonnet", env.ThreadDiscriminatorText(), "the session key keeps hashing the opening message")

	res, found := env.ExtractForceModelCommand()
	require.True(t, found)
	assert.True(t, res.Clear)
}

func TestStripHistoricalCommands_AnthropicBlocks(t *testing.T) {
	body, err := json.Marshal(map[string]any{
		"model": "claude-sonnet-5-5", "max_tokens": 10,
		"messages": []any{
			map[string]any{"role": "user", "content": "hello"},
			map[string]any{"role": "assistant", "content": "hi"},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": "<system-reminder>be nice</system-reminder>"},
				map[string]any{"type": "text", "text": "/force-model opus"},
			}},
			map[string]any{"role": "assistant", "content": "pinned"},
			map[string]any{"role": "user", "content": "go"},
		},
	})
	require.NoError(t, err)
	env, err := ParseAnthropic(body)
	require.NoError(t, err)
	removed, err := env.StripHistoricalCommands()
	require.NoError(t, err)
	assert.Equal(t, 1, removed)
	msgs := gjson.GetBytes(env.body, "messages").Array()
	require.Len(t, msgs, 4, "a message holding only wrappers and a directive is dropped whole")
	assert.Equal(t, "assistant", msgs[2].Get("role").Str)
}

func TestStripHistoricalCommands_NoDirectivesIsByteIdentical(t *testing.T) {
	body, err := json.Marshal(map[string]any{
		"model": "gpt-5.6-sol",
		"messages": []any{
			map[string]any{"role": "user", "content": "hello"},
			map[string]any{"role": "assistant", "content": "hi"},
			map[string]any{"role": "user", "content": "bye"},
		},
	})
	require.NoError(t, err)
	env, err := ParseOpenAI(body)
	require.NoError(t, err)
	removed, err := env.StripHistoricalCommands()
	require.NoError(t, err)
	assert.Equal(t, 0, removed)
	assert.Equal(t, body, env.body)
}

func TestStripHistoricalCommands_OpeningDirectiveKeepsThreadDiscriminator(t *testing.T) {
	env, err := ParseAnthropic([]byte(`{"model":"claude","messages":[` +
		`{"role":"user","content":"/fm astra"},` +
		`{"role":"assistant","content":"Weave Router: force-model applied: astra."},` +
		`{"role":"user","content":"now write the parser"}]}`))
	require.NoError(t, err)
	require.Equal(t, "/fm astra", env.ThreadDiscriminatorText())

	removed, err := env.StripHistoricalCommands()
	require.NoError(t, err)
	assert.Equal(t, 2, removed)
	assert.Equal(t, "now write the parser", env.FirstUserMessageText())
	assert.Equal(t, "/fm astra", env.ThreadDiscriminatorText(), "the session key must keep hashing what the client sent")
	assert.Equal(t, "/fm astra", env.Clone().ThreadDiscriminatorText())
	assert.Equal(t, int64(1), gjson.GetBytes(env.body, "messages.#").Int(), "the conversation must not open on the orphaned ack")
}

func TestStripHistoricalCommands_OpeningMixedMessageKeepsRemainder(t *testing.T) {
	env, err := ParseOpenAI([]byte(`{"model":"gpt","messages":[` +
		`{"role":"system","content":"be terse"},` +
		`{"role":"user","content":"/fm astra\nplease refactor the parser"},` +
		`{"role":"assistant","content":"done"},` +
		`{"role":"user","content":"thanks"}]}`))
	require.NoError(t, err)
	removed, err := env.StripHistoricalCommands()
	require.NoError(t, err)
	assert.Equal(t, 1, removed)
	msgs := gjson.GetBytes(env.body, "messages").Array()
	require.Len(t, msgs, 4)
	assert.Equal(t, "please refactor the parser", msgs[1].Get("content").Str)
	assert.Equal(t, "done", msgs[2].Get("content").Str)
}

func TestStripHistoricalCommands_OpeningDirectiveKeepsAssistantToolCall(t *testing.T) {
	env, err := ParseAnthropic([]byte(`{"model":"claude","messages":[` +
		`{"role":"user","content":"/fm astra"},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"read","input":{}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]},` +
		`{"role":"user","content":"now write the parser"}]}`))
	require.NoError(t, err)
	_, err = env.StripHistoricalCommands()
	require.NoError(t, err)
	msgs := gjson.GetBytes(env.body, "messages").Array()
	require.Len(t, msgs, 3)
	assert.Equal(t, "tool_use", msgs[0].Get("content.0.type").Str, "a tool call must keep its paired tool result")
}
