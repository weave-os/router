package recordedlaws_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type scriptTurn struct {
	role   string
	text   string
	call   *toolCall
	result *toolResult
	item   json.RawMessage
}

type script struct {
	system string
	tools  []gjson.Result
	turns  []scriptTurn
}

func recordedScript(t *testing.T) script {
	t.Helper()
	echo := richestResponsesEcho(t)
	reply := echo.Get(`output.#(type=="message")`)
	require.True(t, reply.Exists(), "the richest Responses cassette has no assistant message")
	call := anthropicTurn(gjson.ParseBytes(assembleAnthropicSSE(realToolUseRecording(t).body))).tools[0]
	return script{
		system: echo.Get("instructions").String(),
		tools:  echo.Get("tools").Array(),
		turns: []scriptTurn{
			{role: "user", text: "List the working directory."},
			{role: "assistant", call: &call},
			{role: "user", result: &toolResult{id: call.id, content: "total 0"}},
			{role: "assistant", text: joinedText(reply.Get("content"), "output_text"), item: json.RawMessage(reply.Raw)},
			{role: "user", text: "Thanks."},
		},
	}
}

func (s script) anthropicBody() []byte {
	tools := make([]any, 0, len(s.tools))
	for _, tool := range s.tools {
		tools = append(tools, map[string]any{"name": tool.Get("name").String(), "description": tool.Get("description").String(), "input_schema": json.RawMessage(tool.Get("parameters").Raw)})
	}
	messages := make([]any, 0, len(s.turns))
	for _, turn := range s.turns {
		var block map[string]any
		switch {
		case turn.call != nil:
			block = map[string]any{"type": "tool_use", "id": turn.call.id, "name": turn.call.name, "input": json.RawMessage(turn.call.args)}
		case turn.result != nil:
			block = map[string]any{"type": "tool_result", "tool_use_id": turn.result.id, "content": turn.result.content}
		default:
			block = map[string]any{"type": "text", "text": turn.text}
		}
		messages = append(messages, map[string]any{"role": turn.role, "content": []any{block}})
	}
	return mustMarshal(map[string]any{"model": clientModel, "max_tokens": 1024, "system": []any{map[string]any{"type": "text", "text": s.system}}, "tools": tools, "messages": messages})
}

func (s script) chatBody() []byte {
	tools := make([]any, 0, len(s.tools))
	for _, tool := range s.tools {
		tools = append(tools, map[string]any{"type": "function", "function": map[string]any{"name": tool.Get("name").String(), "description": tool.Get("description").String(), "parameters": json.RawMessage(tool.Get("parameters").Raw)}})
	}
	messages := []any{map[string]any{"role": "system", "content": s.system}}
	for _, turn := range s.turns {
		switch {
		case turn.call != nil:
			messages = append(messages, map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{"id": turn.call.id, "type": "function", "function": map[string]any{"name": turn.call.name, "arguments": turn.call.args}}}})
		case turn.result != nil:
			messages = append(messages, map[string]any{"role": "tool", "tool_call_id": turn.result.id, "content": turn.result.content})
		default:
			messages = append(messages, map[string]any{"role": turn.role, "content": turn.text})
		}
	}
	return mustMarshal(map[string]any{"model": clientModel, "max_tokens": 1024, "tools": tools, "messages": messages})
}

func (s script) responsesBody() []byte {
	tools := make([]any, 0, len(s.tools))
	for _, tool := range s.tools {
		tools = append(tools, json.RawMessage(tool.Raw))
	}
	input := make([]any, 0, len(s.turns))
	for _, turn := range s.turns {
		switch {
		case turn.call != nil:
			input = append(input, map[string]any{"type": "function_call", "call_id": turn.call.id, "name": turn.call.name, "arguments": turn.call.args})
		case turn.result != nil:
			input = append(input, map[string]any{"type": "function_call_output", "call_id": turn.result.id, "output": turn.result.content})
		case turn.item != nil:
			input = append(input, turn.item)
		default:
			input = append(input, map[string]any{"type": "message", "role": turn.role, "content": []any{map[string]any{"type": "input_text", "text": turn.text}}})
		}
	}
	return mustMarshal(map[string]any{"model": clientModel, "instructions": s.system, "tools": tools, "input": input, "max_output_tokens": 1024})
}

func (s script) bodies() map[wireFormat][]byte {
	return map[wireFormat][]byte{anthropicReq: s.anthropicBody(), chatReq: s.chatBody(), responsesReq: s.responsesBody()}
}
