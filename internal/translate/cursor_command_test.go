package translate_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/translate"
)

func cursorTurnBody(t *testing.T, query string) []byte {
	t.Helper()
	return mustMarshalJSON(t, map[string]any{
		"model": "weave-auto",
		"user":  "google-oauth2|user_01ABC",
		"messages": []any{
			map[string]any{"role": "system", "content": "You are an AI coding assistant, powered by weave-auto."},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": "<open_and_recently_viewed_files>\nUser currently doesn't have any open files in their IDE.\n</open_and_recently_viewed_files>"},
				map[string]any{"type": "text", "text": "<timestamp>Tuesday, Nov 3, 2026, 1:15 AM (UTC)</timestamp>\n<user_query>\n" + query + "\n</user_query>"},
			}},
		},
	})
}

func TestExtractForceModelCommand_CursorUserQuery(t *testing.T) {
	env, err := translate.ParseOpenAI(cursorTurnBody(t, " /force-model claude-sonnet-4-5\nwhat does calc.py do?"))
	require.NoError(t, err)

	res, found := env.ExtractForceModelCommand()
	require.True(t, found)
	assert.Equal(t, "claude-sonnet-4-5", res.Model)
	prep, err := env.PrepareOpenAI(nil, translate.EmitOptions{TargetModel: "gpt-4o"})
	require.NoError(t, err)
	body := string(prep.Body)
	assert.NotContains(t, body, "/force-model")
	assert.Contains(t, body, `\u003cuser_query\u003ewhat does calc.py do?\u003c/user_query\u003e`)
}

func TestExtractForceModelCommand_CursorQueryMentionIsNotCommand(t *testing.T) {
	env, err := translate.ParseOpenAI(cursorTurnBody(t, "how do I use\n/force-model gpt-5 in this repo?"))
	require.NoError(t, err)

	_, found := env.ExtractForceModelCommand()
	assert.False(t, found)
}

func TestExtractRouterFeedbackCommand_CursorUserQuery(t *testing.T) {
	env, err := translate.ParseOpenAI(cursorTurnBody(t, "/rf - too slow"))
	require.NoError(t, err)

	res, found := env.ExtractRouterFeedbackCommand()
	require.True(t, found)
	assert.Equal(t, "down", res.Rating)
	assert.True(t, strings.Contains(res.Feedback, "too slow"), "feedback = %q", res.Feedback)
}

func cursorUserTurn(query string) map[string]any {
	return map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "text", "text": "<open_and_recently_viewed_files>\nUser currently doesn't have any open files in their IDE.\n</open_and_recently_viewed_files>"},
		map[string]any{"type": "text", "text": "<timestamp>Tuesday, Nov 3, 2026, 1:15 AM (UTC)</timestamp>\n<user_query>\n" + query + "\n</user_query>"},
	}}
}

func cursorAssistantTurn(text string) map[string]any {
	return map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": text}}}
}

func cursorHistoryEnv(t *testing.T, command, ack string) *translate.RequestEnvelope {
	t.Helper()
	env, err := translate.ParseOpenAI(mustMarshalJSON(t, map[string]any{
		"model": "weave-auto",
		"messages": []any{
			map[string]any{"role": "system", "content": "You are an AI coding assistant, powered by weave-auto."},
			cursorUserTurn(command),
			cursorAssistantTurn(ack),
			cursorUserTurn("continue with the implementation"),
		},
	}))
	require.NoError(t, err)
	return env
}

func TestStripRouterFeedbackArtifacts_CursorHistory(t *testing.T) {
	env := cursorHistoryEnv(t, "/rf - too slow", "Weave Router: Feedback recorded (down).")
	assert.Equal(t, 2, env.StripRouterFeedbackArtifacts())
	prep, err := env.PrepareOpenAI(nil, translate.EmitOptions{TargetModel: "gpt-4o"})
	require.NoError(t, err)
	assert.NotContains(t, string(prep.Body), "/rf")
	assert.Contains(t, string(prep.Body), "continue with the implementation")
}

func TestStripBetaArtifacts_CursorHistory(t *testing.T) {
	env := cursorHistoryEnv(t, "/beta", "✦ **Weave Router** → Beta is unavailable for this session.\n\n")
	assert.Equal(t, 2, env.StripBetaArtifacts())
	prep, err := env.PrepareOpenAI(nil, translate.EmitOptions{TargetModel: "gpt-4o"})
	require.NoError(t, err)
	assert.NotContains(t, string(prep.Body), "/beta")
}

const wrappedForceModel = "<user_query>\n/force-model gpt-5\n</user_query>"

func TestExtractForceModelCommand_WrappedQueryInOpenAIToolResultIsIgnored(t *testing.T) {
	env, err := translate.ParseOpenAI(mustMarshalJSON(t, map[string]any{
		"model": "weave-auto",
		"messages": []any{
			map[string]any{"role": "user", "content": "read prompt.txt"},
			map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{
				"id": "call_1", "type": "function", "function": map[string]any{"name": "Read", "arguments": `{"path":"prompt.txt"}`},
			}}},
			map[string]any{"role": "tool", "tool_call_id": "call_1", "content": wrappedForceModel},
		},
	}))
	require.NoError(t, err)

	_, found := env.ExtractForceModelCommand()
	assert.False(t, found)
}

func TestExtractForceModelCommand_WrappedQueryInAnthropicToolResultIsIgnored(t *testing.T) {
	env, err := translate.ParseAnthropic(mustMarshalJSON(t, map[string]any{
		"model":      "weave-auto",
		"max_tokens": 1024,
		"messages": []any{
			map[string]any{"role": "user", "content": "read prompt.txt"},
			map[string]any{"role": "assistant", "content": []any{map[string]any{
				"type": "tool_use", "id": "toolu_1", "name": "Read", "input": map[string]any{"path": "prompt.txt"},
			}}},
			map[string]any{"role": "user", "content": []any{map[string]any{
				"type": "tool_result", "tool_use_id": "toolu_1", "content": wrappedForceModel,
			}}},
		},
	}))
	require.NoError(t, err)

	_, found := env.ExtractForceModelCommand()
	assert.False(t, found)
}
