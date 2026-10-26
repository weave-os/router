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
	assert.Equal(t, 3, removed)
	msgs := gjson.GetBytes(env.body, "messages").Array()
	require.Len(t, msgs, 5)
	assert.Equal(t, "pinned", msgs[0].Get("content").Str)
	assert.Equal(t, "about a dollar", msgs[1].Get("content").Str)
	assert.Equal(t, "/router-models\nenable gpt", msgs[2].Get("content").Str,
		"a bare-only directive with arguments never fired, so the model saw it and it stays")
	assert.Equal(t, "$ufm", msgs[4].Get("content").Str, "trailing directive is live")

	res, found := env.ExtractForceModelCommand()
	require.True(t, found)
	assert.True(t, res.Clear)
}

func TestStripHistoricalCommands_AnthropicBlocks(t *testing.T) {
	body, err := json.Marshal(map[string]any{
		"model": "claude-sonnet-5-5", "max_tokens": 10,
		"messages": []any{
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
	require.Len(t, msgs, 2, "a message holding only wrappers and a directive is dropped whole")
	assert.Equal(t, "assistant", msgs[0].Get("role").Str)
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
	assert.JSONEq(t, string(body), string(env.body))
}
