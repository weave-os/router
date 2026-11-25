package translate_test

import (
	"testing"

	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestStripRetiredBetaHistoryAnthropicKeepsTrailingPrompt(t *testing.T) {
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

	assert.Equal(t, 2, env.StripRetiredBetaHistory())
	prepared, err := env.PrepareAnthropic(nil, translate.EmitOptions{TargetModel: "claude-sonnet-5"})
	require.NoError(t, err)
	assert.Equal(t, int64(3), gjson.GetBytes(prepared.Body, "messages.#").Int())
	assert.Equal(t, "/beta", gjson.GetBytes(prepared.Body, "messages.2.content.0.text").String(),
		"a /beta typed now is an ordinary prompt and reaches the model")
}

func TestStripRetiredBetaHistoryOpenAI(t *testing.T) {
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

	assert.Equal(t, 2, env.StripRetiredBetaHistory())
	prepared, err := env.PrepareOpenAI(nil, translate.EmitOptions{TargetModel: "gpt-5.6-sol"})
	require.NoError(t, err)
	assert.Equal(t, int64(3), gjson.GetBytes(prepared.Body, "messages.#").Int())
	assert.Equal(t, "continue with the implementation", gjson.GetBytes(prepared.Body, "messages.2.content").String())
}

func TestStripRetiredBetaHistoryRemovesInvalidControlTurnButKeepsDiscussion(t *testing.T) {
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

	assert.Equal(t, 2, env.StripRetiredBetaHistory())
	prepared, err := env.PrepareAnthropic(nil, translate.EmitOptions{TargetModel: "claude-sonnet-5"})
	require.NoError(t, err)
	assert.Equal(t, int64(3), gjson.GetBytes(prepared.Body, "messages.#").Int())
	assert.Equal(t, "Please explain /beta instead of toggling it.", gjson.GetBytes(prepared.Body, "messages.0.content").String())
	assert.Contains(t, gjson.GetBytes(prepared.Body, "messages.1.content").String(), "quoted documentation")
}

func TestStripRetiredBetaHistoryKeepsEmptyAssistantTurnNotFollowingACommand(t *testing.T) {
	body := mustMarshalJSON(t, map[string]any{
		"model": "claude-sonnet-5",
		"messages": []any{
			map[string]any{"role": "user", "content": "hello"},
			map[string]any{"role": "assistant", "content": ""},
			map[string]any{"role": "user", "content": "/beta"},
			map[string]any{"role": "assistant", "content": ""},
			map[string]any{"role": "user", "content": "continue"},
		},
		"max_tokens": 128,
	})
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)

	assert.Equal(t, 2, env.StripRetiredBetaHistory())
}

func TestStripRetiredBetaHistoryKeepsBetaPromptAnsweredByAModel(t *testing.T) {
	body := mustMarshalJSON(t, map[string]any{
		"model": "claude-sonnet-5",
		"messages": []any{
			map[string]any{"role": "user", "content": "/beta"},
			map[string]any{"role": "assistant", "content": "There is no /beta command in this project."},
			map[string]any{"role": "user", "content": "/beta-llm"},
			map[string]any{"role": "assistant", "content": "✦ **Weave Router** → Qwen beta enabled. Type /beta-llm again to turn it off.\n\n"},
			map[string]any{"role": "user", "content": "continue"},
		},
		"max_tokens": 128,
	})
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)

	assert.Equal(t, 2, env.StripRetiredBetaHistory())
	prepared, err := env.PrepareAnthropic(nil, translate.EmitOptions{TargetModel: "claude-sonnet-5"})
	require.NoError(t, err)
	assert.Equal(t, int64(3), gjson.GetBytes(prepared.Body, "messages.#").Int())
	assert.Equal(t, "/beta", gjson.GetBytes(prepared.Body, "messages.0.content").String())
	assert.Equal(t, "There is no /beta command in this project.", gjson.GetBytes(prepared.Body, "messages.1.content").String())
}

func TestStripRetiredBetaHistoryKeepsToolCallAnswerToBetaPrompt(t *testing.T) {
	body := mustMarshalJSON(t, map[string]any{
		"model": "gpt-5.6-sol",
		"messages": []any{
			map[string]any{"role": "user", "content": "/beta"},
			map[string]any{
				"role":    "assistant",
				"content": "",
				"tool_calls": []any{map[string]any{
					"id": "call_1", "type": "function",
					"function": map[string]any{"name": "search", "arguments": `{"q":"beta"}`},
				}},
			},
			map[string]any{"role": "tool", "tool_call_id": "call_1", "content": "no results"},
			map[string]any{"role": "user", "content": "continue"},
		},
	})
	env, err := translate.ParseOpenAI(body)
	require.NoError(t, err)

	assert.Equal(t, 0, env.StripRetiredBetaHistory(),
		"a model's tool-call answer to /beta is not a router reply and keeps its tool result paired")
}

func TestStripRetiredBetaHistoryRemovesPinnedAndRoutingModelReplies(t *testing.T) {
	for name, reply := range map[string]string{
		"pinned":        "✦ **Weave Router** → Beta is always on for this installation; /beta has no effect.\n\n",
		"routing model": "✦ **Weave Router** → Select a router with `wv router set-model weave-hmm-beta`, or `wv router set-model weave-auto`, then start a new session.\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			body := mustMarshalJSON(t, map[string]any{
				"model": "gpt-5.6-sol",
				"messages": []any{
					map[string]any{"role": "user", "content": "/beta"},
					map[string]any{"role": "assistant", "content": reply},
					map[string]any{"role": "user", "content": "continue with the implementation"},
				},
			})
			env, err := translate.ParseOpenAI(body)
			require.NoError(t, err)

			assert.Equal(t, 2, env.StripRetiredBetaHistory())
		})
	}
}

func TestStripRetiredBetaHistoryKeepsAdaptedRoutingModelNotice(t *testing.T) {
	adapted := "✦ **Weave Router** → Select a router with `wv router set-model weave-auto`.\n" +
		"Save your work first, then start a new session."
	body := mustMarshalJSON(t, map[string]any{
		"model": "gpt-5.6-sol",
		"messages": []any{
			map[string]any{"role": "user", "content": "adapt this router notice for my workspace"},
			map[string]any{"role": "assistant", "content": adapted},
			map[string]any{"role": "user", "content": "why save work first?"},
		},
	})
	env, err := translate.ParseOpenAI(body)
	require.NoError(t, err)

	assert.Equal(t, 0, env.StripRetiredBetaHistory())
}

func TestStripRetiredBetaHistoryKeepsPinnedNoticeNotFollowingACommand(t *testing.T) {
	body := mustMarshalJSON(t, map[string]any{
		"model": "gpt-5.6-sol",
		"messages": []any{
			map[string]any{"role": "user", "content": "repeat the router notice verbatim"},
			map[string]any{"role": "assistant", "content": "✦ **Weave Router** → Beta is always on for this installation; /beta has no effect."},
			map[string]any{"role": "user", "content": "thanks"},
		},
	})
	env, err := translate.ParseOpenAI(body)
	require.NoError(t, err)

	assert.Equal(t, 0, env.StripRetiredBetaHistory())
}

func TestStripRetiredBetaHistoryKeepsAckTextCarryingToolCalls(t *testing.T) {
	body := mustMarshalJSON(t, map[string]any{
		"model": "gpt-5.6-sol",
		"messages": []any{
			map[string]any{"role": "user", "content": "check the repo"},
			map[string]any{
				"role":    "assistant",
				"content": "✦ **Weave Router** → Usage: /beta",
				"tool_calls": []any{map[string]any{
					"id": "call_1", "type": "function",
					"function": map[string]any{"name": "read_file", "arguments": `{"path":"README.md"}`},
				}},
			},
			map[string]any{"role": "tool", "tool_call_id": "call_1", "content": "readme"},
			map[string]any{"role": "user", "content": "continue"},
		},
	})
	env, err := translate.ParseOpenAI(body)
	require.NoError(t, err)

	assert.Equal(t, 0, env.StripRetiredBetaHistory())
}
