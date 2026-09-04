package translate_test

import (
	"encoding/json"
	"testing"

	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseForceModelCommand_ForceModel(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		wantModel    string
		wantFound    bool
		wantStripped string
	}{
		{
			name:         "command only",
			input:        "/force-model deepseek/deepseek-v4-pro",
			wantModel:    "deepseek/deepseek-v4-pro",
			wantFound:    true,
			wantStripped: "",
		},
		{
			name:         "command with trailing text",
			input:        "/force-model claude-opus-4-7\nPlease help me with this.",
			wantModel:    "claude-opus-4-7",
			wantFound:    true,
			wantStripped: "Please help me with this.",
		},
		{
			name:         "same-line text is part of the model name",
			input:        "/force-model gpt-5 help me debug this",
			wantModel:    "gpt-5 help me debug this",
			wantFound:    true,
			wantStripped: "",
		},
		{
			name:         "multi-word model name is kept whole",
			input:        "/fm qwen 3.8",
			wantModel:    "qwen 3.8",
			wantFound:    true,
			wantStripped: "",
		},
		{
			name:         "internal whitespace runs collapse",
			input:        "/fm  qwen    3.8",
			wantModel:    "qwen 3.8",
			wantFound:    true,
			wantStripped: "",
		},
		{
			// A next-line prompt is the supported way to pin and prompt at once.
			name:         "next-line prompt is preserved",
			input:        "/force-model gpt-5\nhelp me debug this",
			wantModel:    "gpt-5",
			wantFound:    true,
			wantStripped: "help me debug this",
		},
		{
			// /fm is the router-side alias for clients without local
			// slash-command expansion (pi, opencode, raw API callers).
			name:         "fm alias",
			input:        "/fm haiku",
			wantModel:    "haiku",
			wantFound:    true,
			wantStripped: "",
		},
		{
			// /fmt (or any /fm<x>) must not match the alias prefix.
			name:      "fm alias without space boundary is ignored",
			input:     "/fmt this file",
			wantFound: false,
		},
		{
			// Only a leading /force-model triggers; pasted content often has
			// lines starting with "/" and must not rewrite session routing.
			name:      "command after leading text is ignored",
			input:     "Please help me.\n/force-model gemini-2.5-pro",
			wantFound: false,
		},
		{
			name:         "leading blank lines before command",
			input:        "\n\n/force-model claude-opus-4-7\nthen help",
			wantModel:    "claude-opus-4-7",
			wantFound:    true,
			wantStripped: "then help",
		},
		{
			name:      "no command",
			input:     "Can you help me debug this code?",
			wantFound: false,
		},
		{
			name:      "force-model without model name is ignored",
			input:     "/force-model ",
			wantFound: false,
		},
		{
			name:         "leading and trailing whitespace on command line",
			input:        "  /force-model   qwen/qwen3-235b-a22b-2507  ",
			wantModel:    "qwen/qwen3-235b-a22b-2507",
			wantFound:    true,
			wantStripped: "",
		},
		{
			// Injected <system-reminder> blocks must not block recognition
			// and must be preserved in the stripped output.
			name:         "leading system-reminder before command",
			input:        "<system-reminder>be helpful</system-reminder>\n/force-model gpt-5",
			wantModel:    "gpt-5",
			wantFound:    true,
			wantStripped: "<system-reminder>be helpful</system-reminder>",
		},
		{
			name:         "multiple leading injected tag blocks",
			input:        "<system-reminder>foo</system-reminder>\n<command-name>x</command-name>\n/force-model claude-opus-4-7\nthen help",
			wantModel:    "claude-opus-4-7",
			wantFound:    true,
			wantStripped: "<system-reminder>foo</system-reminder>\n<command-name>x</command-name>\nthen help",
		},
		{
			name:         "multiline system-reminder body",
			input:        "<system-reminder>\nline one\nline two\n</system-reminder>\n/force-model gemini-2.5-pro",
			wantModel:    "gemini-2.5-pro",
			wantFound:    true,
			wantStripped: "<system-reminder>\nline one\nline two\n</system-reminder>",
		},
		{
			// Security guard preserved: an unclosed tag does not satisfy the
			// prefix matcher, so a stray /force-model after it is still ignored.
			name:      "unclosed tag does not unlock leading-line guard",
			input:     "<system-reminder>unclosed\n/force-model gpt-5",
			wantFound: false,
		},
		{
			// Attributed tags aren't part of Claude Code's injection set and
			// may be pasted HTML/XML, so they must not unlock the guard.
			name:      "tag with attributes does not unlock leading-line guard",
			input:     "<div class=\"x\">hi</div>\n/force-model gpt-5",
			wantFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, err := translate.ParseAnthropic(buildAnthropicBody(t, tt.input))
			require.NoError(t, err)

			res, found := env.ExtractForceModelCommand()
			assert.Equal(t, tt.wantFound, found)
			if !tt.wantFound {
				return
			}
			assert.Equal(t, tt.wantModel, res.Model)
			assert.False(t, res.Clear)
			assert.False(t, res.FromToolResult)

			// Verify the command was stripped from env body.
			stripped := lastUserMessageText(t, env)
			assert.Equal(t, tt.wantStripped, stripped)
		})
	}
}

func TestParseForceModelCommand_UnforceModel(t *testing.T) {
	for _, input := range []string{"/unforce-model", "/ufm", "$unforce-model", "$ufm"} {
		t.Run(input, func(t *testing.T) {
			env, err := translate.ParseAnthropic(buildAnthropicBody(t, input))
			require.NoError(t, err)

			res, found := env.ExtractForceModelCommand()
			require.True(t, found)
			assert.True(t, res.Clear)
			assert.Empty(t, res.Model)
		})
	}
}

func TestExtractForceModelCommand_DollarAliases(t *testing.T) {
	for _, input := range []string{"$force-model gpt-5", "$fm gpt-5"} {
		t.Run(input, func(t *testing.T) {
			env, err := translate.ParseAnthropic(buildAnthropicBody(t, input))
			require.NoError(t, err)
			res, found := env.ExtractForceModelCommand()
			require.True(t, found)
			assert.Equal(t, "gpt-5", res.Model)
		})
	}
}

func TestExtractForceModelCommand_CodexExecPreamble(t *testing.T) {
	body := mustMarshalJSON(t, map[string]any{
		"model": "gpt-5.6-sol",
		"messages": []any{
			map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
				"id": "call_skill", "type": "function", "function": map[string]any{"name": "exec", "arguments": "{}"},
			}}},
			map[string]any{"role": "tool", "tool_call_id": "call_skill", "content": "Script completed\nWall time: 0.0 seconds\nOutput:\n /force-model gpt-5"},
		},
	})
	env, err := translate.ParseOpenAI(body)
	require.NoError(t, err)
	res, found := env.ExtractForceModelCommand()
	require.True(t, found)
	assert.True(t, res.FromToolResult)
	assert.Equal(t, "gpt-5", res.Model)
}

func TestExtractForceModelCommand_CodexExecDocumentationIsNotCommand(t *testing.T) {
	body := mustMarshalJSON(t, map[string]any{
		"model": "gpt-5.6-sol",
		"messages": []any{
			map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
				"id": "call_skill", "type": "function", "function": map[string]any{"name": "exec", "arguments": "{}"},
			}}},
			map[string]any{"role": "tool", "tool_call_id": "call_skill", "content": "Script completed\nOutput:\n---\nname: force-model\n```text\n /force-model gpt-5\n```"},
		},
	})
	env, err := translate.ParseOpenAI(body)
	require.NoError(t, err)
	_, found := env.ExtractForceModelCommand()
	assert.False(t, found, "a command example in exec output must not pin the session")
}

func TestExtractForceModelCommand_OpenAIFormat(t *testing.T) {
	body := mustMarshalJSON(t, map[string]any{
		"model": "gpt-4o",
		"messages": []any{
			map[string]any{"role": "user", "content": "/force-model gpt-5\nhelp me."},
		},
	})
	env, err := translate.ParseOpenAI(body)
	require.NoError(t, err)

	res, found := env.ExtractForceModelCommand()
	require.True(t, found)
	assert.Equal(t, "gpt-5", res.Model)
	assert.False(t, res.Clear)
	assert.Equal(t, "help me.", lastOpenAIUserMessageText(t, env))
}

func TestExtractForceModelCommand_ArrayContent(t *testing.T) {
	body := mustMarshalJSON(t, map[string]any{
		"model": "claude-sonnet-4-6",
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "text", "text": "/force-model gpt-5\nHelp me."},
				},
			},
		},
		"max_tokens": 1024,
	})
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)

	res, found := env.ExtractForceModelCommand()
	require.True(t, found)
	assert.Equal(t, "gpt-5", res.Model)

	stripped := lastUserMessageArrayText(t, env)
	assert.Equal(t, "Help me.", stripped)
}

func TestExtractForceModelCommand_ArrayContentMultipleTextBlocks(t *testing.T) {
	// The directive can land in a non-first text block (e.g. after an
	// injected <command-name> block), so the parser must scan all of them.
	body := mustMarshalJSON(t, map[string]any{
		"model": "claude-sonnet-4-6",
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "text", "text": "<command-message>force-model</command-message>\n<command-name>/force-model</command-name>\n<command-args>qwen/qwen3.6-35b-a3b</command-args>"},
					map[string]any{"type": "text", "text": "/force-model qwen/qwen3.6-35b-a3b"},
				},
			},
		},
		"max_tokens": 1024,
	})
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)

	res, found := env.ExtractForceModelCommand()
	require.True(t, found, "directive in a non-first text block must still be recognized")
	assert.Equal(t, "qwen/qwen3.6-35b-a3b", res.Model)

	// The directive block should be empty (or stripped) after extraction.
	raw, _ := env.PrepareAnthropic(nil, translate.EmitOptions{TargetModel: "claude-sonnet-4-6"})
	var got map[string]any
	require.NoError(t, json.Unmarshal(raw.Body, &got))
	msgs, _ := got["messages"].([]any)
	last, _ := msgs[len(msgs)-1].(map[string]any)
	blocks, _ := last["content"].([]any)
	require.Len(t, blocks, 2)
	second, _ := blocks[1].(map[string]any)
	assert.Equal(t, "", second["text"], "the directive-bearing text block must be stripped")
}

func TestExtractForceModelCommand_AgentToolResultString(t *testing.T) {
	body := mustMarshalJSON(t, map[string]any{
		"model": "claude-sonnet-4-6",
		"messages": []any{
			map[string]any{
				"role": "assistant",
				"content": []any{map[string]any{
					"type": "tool_use", "id": "toolu_skill", "name": "Skill", "input": map[string]any{"skill": "fm"},
				}},
			},
			map[string]any{"role": "user", "content": "/force-model opus"},
		},
		"max_tokens": 1024,
	})
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)

	cmd, found := env.ExtractForceModelCommand()
	require.True(t, found)
	assert.Equal(t, "opus", cmd.Model)
	assert.True(t, cmd.FromToolResult)
	assert.Equal(t, "", lastUserMessageText(t, env))
}

func TestExtractForceModelCommand_AgentToolResultBlock(t *testing.T) {
	body := mustMarshalJSON(t, map[string]any{
		"model": "claude-sonnet-4-6",
		"messages": []any{
			map[string]any{
				"role": "assistant",
				"content": []any{map[string]any{
					"type": "tool_use", "id": "toolu_skill", "name": "Skill", "input": map[string]any{"skill": "fm"},
				}},
			},
			map[string]any{
				"role": "user",
				"content": []any{map[string]any{
					"type": "tool_result", "tool_use_id": "toolu_skill", "content": "/force-model opus",
				}},
			},
		},
		"max_tokens": 1024,
	})
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)

	cmd, found := env.ExtractForceModelCommand()
	require.True(t, found)
	assert.Equal(t, "opus", cmd.Model)
	assert.True(t, cmd.FromToolResult)
}

func TestExtractForceModelCommand_AgentSoleTextBlockDropsWholeMessage(t *testing.T) {
	body := mustMarshalJSON(t, map[string]any{
		"model": "claude-sonnet-4-6",
		"messages": []any{
			map[string]any{"role": "user", "content": "analyze usage"},
			map[string]any{
				"role": "assistant",
				"content": []any{map[string]any{
					"type": "tool_use", "id": "toolu_skill", "name": "Skill", "input": map[string]any{"skill": "fm"},
				}},
			},
			map[string]any{
				"role":    "user",
				"content": []any{map[string]any{"type": "text", "text": "/force-model opus"}},
			},
		},
		"max_tokens": 1024,
	})
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)

	cmd, found := env.ExtractForceModelCommand()
	require.True(t, found)
	assert.Equal(t, "opus", cmd.Model)
	assert.True(t, cmd.FromToolResult)

	msgs := anthropicMessages(t, env)
	require.Len(t, msgs, 2, "the command-only user message must be dropped, not left with empty content")
	for _, msg := range msgs {
		if blocks, ok := msg["content"].([]any); ok {
			assert.NotEmpty(t, blocks, "no message may be left with an empty content array")
		}
	}
}

func TestExtractForceModelCommand_AgentToolResultSiblingBlockSurvives(t *testing.T) {
	body := mustMarshalJSON(t, map[string]any{
		"model": "claude-sonnet-4-6",
		"messages": []any{
			map[string]any{
				"role": "assistant",
				"content": []any{map[string]any{
					"type": "tool_use", "id": "toolu_skill", "name": "Skill", "input": map[string]any{"skill": "fm"},
				}},
			},
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "tool_result", "tool_use_id": "toolu_skill", "content": "Launching skill: fm"},
					map[string]any{"type": "text", "text": "/force-model opus"},
				},
			},
		},
		"max_tokens": 1024,
	})
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)

	cmd, found := env.ExtractForceModelCommand()
	require.True(t, found)
	assert.Equal(t, "opus", cmd.Model)
	assert.True(t, cmd.FromToolResult)

	msgs := anthropicMessages(t, env)
	require.Len(t, msgs, 2)
	blocks, ok := msgs[1]["content"].([]any)
	require.True(t, ok)
	require.Len(t, blocks, 1, "only the command block is dropped")
	block, _ := blocks[0].(map[string]any)
	assert.Equal(t, "tool_result", block["type"], "the tool_result pairing the assistant tool_use must survive")
}

func TestExtractForceModelCommand_OpenAIToolResult(t *testing.T) {
	body := mustMarshalJSON(t, map[string]any{
		"model": "gpt-4o",
		"messages": []any{
			map[string]any{"role": "user", "content": "analyze usage"},
			map[string]any{
				"role": "assistant",
				"tool_calls": []any{map[string]any{
					"id": "call_skill", "type": "function",
					"function": map[string]any{"name": "Skill", "arguments": `{"skill":"fm"}`},
				}},
			},
			map[string]any{"role": "tool", "tool_call_id": "call_skill", "content": "/force-model gpt-5"},
		},
	})
	env, err := translate.ParseOpenAI(body)
	require.NoError(t, err)

	cmd, found := env.ExtractForceModelCommand()
	require.True(t, found)
	assert.Equal(t, "gpt-5", cmd.Model)
	assert.True(t, cmd.FromToolResult)

	prep, err := env.PrepareOpenAI(nil, translate.EmitOptions{TargetModel: "gpt-5"})
	require.NoError(t, err)
	var prepared map[string]any
	require.NoError(t, json.Unmarshal(prep.Body, &prepared))
	msgs, _ := prepared["messages"].([]any)
	require.Len(t, msgs, 3, "the tool message must remain to answer the assistant tool call")
	tool, _ := msgs[2].(map[string]any)
	assert.Equal(t, "", tool["content"], "only the command text is stripped")
}

func TestExtractForceModelCommand_TrailingSystemNoticeAfterUserTurn(t *testing.T) {
	// Claude Code appends a role:"system" deferred-tools notice after the user
	// turn; it must not make the user's command look historical.
	body := mustMarshalJSON(t, map[string]any{
		"model": "claude-sonnet-4-6",
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "text", "text": "<system-reminder>context</system-reminder>"},
					map[string]any{"type": "text", "text": "/force-model opus"},
				},
			},
			map[string]any{"role": "system", "content": "The following deferred tools are now available."},
		},
		"max_tokens": 1024,
	})
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)

	cmd, found := env.ExtractForceModelCommand()
	require.True(t, found)
	assert.Equal(t, "opus", cmd.Model)
	assert.False(t, cmd.FromToolResult, "a typed command is not agent-issued")
}

func TestExtractForceModelCommand_AgentToolResultWithTrailingSystemNotice(t *testing.T) {
	body := mustMarshalJSON(t, map[string]any{
		"model": "claude-sonnet-4-6",
		"messages": []any{
			map[string]any{"role": "user", "content": "do the thing"},
			map[string]any{"role": "system", "content": "deferred tools notice"},
			map[string]any{
				"role": "assistant",
				"content": []any{map[string]any{
					"type": "tool_use", "id": "toolu_skill", "name": "Skill", "input": map[string]any{"skill": "fm"},
				}},
			},
			map[string]any{
				"role": "user",
				"content": []any{map[string]any{
					"type": "tool_result", "tool_use_id": "toolu_skill", "content": "/force-model opus",
				}},
			},
			map[string]any{"role": "system", "content": "deferred tools notice"},
		},
		"max_tokens": 1024,
	})
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)

	cmd, found := env.ExtractForceModelCommand()
	require.True(t, found)
	assert.Equal(t, "opus", cmd.Model)
	assert.True(t, cmd.FromToolResult, "an interleaved system notice must not hide tool provenance")
}

func TestExtractForceModelCommand_IgnoresHistoricalCommand(t *testing.T) {
	body := mustMarshalJSON(t, map[string]any{
		"model": "claude-sonnet-4-6",
		"messages": []any{
			map[string]any{"role": "user", "content": "/force-model opus"},
			map[string]any{"role": "assistant", "content": "Acknowledged."},
		},
		"max_tokens": 1024,
	})
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)

	_, found := env.ExtractForceModelCommand()
	assert.False(t, found, "a command from an earlier turn must not be replayed")
}

func TestExtractForceModelCommand_NoUserMessage(t *testing.T) {
	body := mustMarshalJSON(t, map[string]any{
		"model": "claude-sonnet-4-6",
		"messages": []any{
			map[string]any{"role": "assistant", "content": "Hello!"},
		},
		"max_tokens": 1024,
	})
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)

	_, found := env.ExtractForceModelCommand()
	assert.False(t, found)
}

func TestExtractForceModelCommand_GeminiFormatIgnored(t *testing.T) {
	body := mustMarshalJSON(t, map[string]any{
		"contents": []any{
			map[string]any{"role": "user", "parts": []any{map[string]any{"text": "/force-model gpt-5"}}},
		},
	})
	env, err := translate.ParseGemini(body)
	require.NoError(t, err)

	_, found := env.ExtractForceModelCommand()
	assert.False(t, found, "Gemini format should not be scanned for force-model commands")
}

// buildAnthropicBody creates a minimal Anthropic Messages request with text as
// the sole user message content.
func buildAnthropicBody(t *testing.T, text string) []byte {
	t.Helper()
	return mustMarshalJSON(t, map[string]any{
		"model": "claude-sonnet-4-6",
		"messages": []any{
			map[string]any{"role": "user", "content": text},
		},
		"max_tokens": 1024,
	})
}

func lastUserMessageText(t *testing.T, env *translate.RequestEnvelope) string {
	t.Helper()
	var body map[string]any
	raw, _ := env.PrepareAnthropic(nil, translate.EmitOptions{TargetModel: "claude-sonnet-4-6"})
	require.NoError(t, json.Unmarshal(raw.Body, &body))
	msgs, _ := body["messages"].([]any)
	for i := len(msgs) - 1; i >= 0; i-- {
		msg, _ := msgs[i].(map[string]any)
		if msg["role"] == "user" {
			if content, ok := msg["content"].(string); ok {
				return content
			}
			blocks, _ := msg["content"].([]any)
			if len(blocks) > 0 {
				block, _ := blocks[len(blocks)-1].(map[string]any)
				if block["type"] == "text" {
					return block["text"].(string)
				}
			}
		}
	}
	return ""
}

func lastUserMessageArrayText(t *testing.T, env *translate.RequestEnvelope) string {
	t.Helper()
	var body map[string]any
	raw, _ := env.PrepareAnthropic(nil, translate.EmitOptions{TargetModel: "claude-sonnet-4-6"})
	require.NoError(t, json.Unmarshal(raw.Body, &body))
	msgs, _ := body["messages"].([]any)
	for i := len(msgs) - 1; i >= 0; i-- {
		msg, _ := msgs[i].(map[string]any)
		if msg["role"] != "user" {
			continue
		}
		blocks, _ := msg["content"].([]any)
		for _, b := range blocks {
			block, _ := b.(map[string]any)
			if block["type"] == "text" {
				return block["text"].(string)
			}
		}
	}
	return ""
}

func anthropicMessages(t *testing.T, env *translate.RequestEnvelope) []map[string]any {
	t.Helper()
	var body map[string]any
	raw, _ := env.PrepareAnthropic(nil, translate.EmitOptions{TargetModel: "claude-sonnet-4-6"})
	require.NoError(t, json.Unmarshal(raw.Body, &body))
	raws, _ := body["messages"].([]any)
	msgs := make([]map[string]any, 0, len(raws))
	for _, m := range raws {
		msg, _ := m.(map[string]any)
		msgs = append(msgs, msg)
	}
	return msgs
}

func mustMarshalJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

func lastOpenAIUserMessageText(t *testing.T, env *translate.RequestEnvelope) string {
	t.Helper()
	prep, err := env.PrepareOpenAI(nil, translate.EmitOptions{TargetModel: "gpt-4o"})
	require.NoError(t, err)

	var body map[string]any
	require.NoError(t, json.Unmarshal(prep.Body, &body))
	msgs, _ := body["messages"].([]any)
	for i := len(msgs) - 1; i >= 0; i-- {
		msg, _ := msgs[i].(map[string]any)
		if msg["role"] == "user" {
			content, _ := msg["content"].(string)
			return content
		}
	}
	return ""
}
