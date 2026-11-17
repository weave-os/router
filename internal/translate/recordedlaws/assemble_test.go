package recordedlaws_test

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/tidwall/gjson"
)

type anthropicBlockBuilder struct {
	block     map[string]any
	text      strings.Builder
	partial   strings.Builder
	thinking  strings.Builder
	signature strings.Builder
}

func (b *anthropicBlockBuilder) apply(delta gjson.Result) {
	switch delta.Get("type").String() {
	case "text_delta":
		b.text.WriteString(delta.Get("text").String())
	case "input_json_delta":
		b.partial.WriteString(delta.Get("partial_json").String())
	case "thinking_delta":
		b.thinking.WriteString(delta.Get("thinking").String())
	case "signature_delta":
		b.signature.WriteString(delta.Get("signature").String())
	}
}

func (b *anthropicBlockBuilder) finish() map[string]any {
	switch b.block["type"] {
	case "text":
		b.block["text"] = stringField(b.block, "text") + b.text.String()
	case "thinking":
		b.block["thinking"] = stringField(b.block, "thinking") + b.thinking.String()
		b.block["signature"] = stringField(b.block, "signature") + b.signature.String()
	case "tool_use":
		if b.partial.Len() > 0 {
			b.block["input"] = json.RawMessage(b.partial.String())
		}
	}
	return b.block
}

func stringField(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

func decodeObject(raw string) map[string]any {
	out := map[string]any{}
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

func mustMarshal(v any) []byte {
	out, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return out
}

func assembleAnthropicSSE(body []byte) []byte {
	message := map[string]any{}
	usage := map[string]any{}
	blocks := map[int64]*anthropicBlockBuilder{}
	for _, frame := range parseSSE(body) {
		data := gjson.Parse(frame.data)
		switch data.Get("type").String() {
		case "message_start":
			message = decodeObject(data.Get("message").Raw)
			usage = decodeObject(data.Get("message.usage").Raw)
		case "content_block_start":
			blocks[data.Get("index").Int()] = &anthropicBlockBuilder{block: decodeObject(data.Get("content_block").Raw)}
		case "content_block_delta":
			if b := blocks[data.Get("index").Int()]; b != nil {
				b.apply(data.Get("delta"))
			}
		case "message_delta":
			for k, v := range decodeObject(data.Get("delta").Raw) {
				message[k] = v
			}
			for k, v := range decodeObject(data.Get("usage").Raw) {
				usage[k] = v
			}
		}
	}
	indexes := make([]int64, 0, len(blocks))
	for i := range blocks {
		indexes = append(indexes, i)
	}
	sort.Slice(indexes, func(a, b int) bool { return indexes[a] < indexes[b] })
	content := make([]any, 0, len(indexes))
	for _, i := range indexes {
		content = append(content, blocks[i].finish())
	}
	message["content"] = content
	message["usage"] = usage
	return mustMarshal(message)
}

type chatToolBuilder struct {
	id, name string
	args     strings.Builder
}

func assembleChatSSE(body []byte) []byte {
	var content, reasoning strings.Builder
	tools := map[int64]*chatToolBuilder{}
	var order []int64
	envelope := map[string]any{"object": "chat.completion"}
	var finish any
	for _, frame := range parseSSE(body) {
		if frame.data == "[DONE]" || !gjson.Valid(frame.data) {
			continue
		}
		data := gjson.Parse(frame.data)
		envelope["id"] = data.Get("id").String()
		if u := data.Get("usage"); u.IsObject() {
			envelope["usage"] = json.RawMessage(u.Raw)
		}
		delta := data.Get("choices.0.delta")
		content.WriteString(delta.Get("content").String())
		reasoning.WriteString(delta.Get("reasoning_content").String())
		for _, tc := range delta.Get("tool_calls").Array() {
			idx := tc.Get("index").Int()
			b := tools[idx]
			if b == nil {
				b = &chatToolBuilder{}
				tools[idx] = b
				order = append(order, idx)
			}
			if id := tc.Get("id").String(); id != "" {
				b.id = id
			}
			if name := tc.Get("function.name").String(); name != "" {
				b.name = name
			}
			b.args.WriteString(tc.Get("function.arguments").String())
		}
		if fr := data.Get("choices.0.finish_reason"); fr.Exists() && fr.Type != gjson.Null {
			finish = fr.String()
		}
	}
	message := map[string]any{"role": "assistant", "content": content.String()}
	if reasoning.Len() > 0 {
		message["reasoning_content"] = reasoning.String()
	}
	var calls []any
	for _, idx := range order {
		b := tools[idx]
		calls = append(calls, map[string]any{"id": b.id, "type": "function", "function": map[string]any{"name": b.name, "arguments": b.args.String()}})
	}
	if len(calls) > 0 {
		message["tool_calls"] = calls
	}
	envelope["choices"] = []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}}
	return mustMarshal(envelope)
}

var responsesTerminalEvents = map[string]bool{"response.completed": true, "response.incomplete": true, "response.failed": true}

func assembleResponsesSSE(body []byte) []byte {
	var terminal gjson.Result
	done := map[int64]json.RawMessage{}
	for _, frame := range parseSSE(body) {
		data := gjson.Parse(frame.data)
		kind := data.Get("type").String()
		if kind == "response.output_item.done" {
			done[data.Get("output_index").Int()] = json.RawMessage(data.Get("item").Raw)
		}
		if responsesTerminalEvents[kind] {
			terminal = data.Get("response")
		}
	}
	response := decodeObject(terminal.Raw)
	if len(terminal.Get("output").Array()) == 0 && len(done) > 0 {
		indexes := make([]int64, 0, len(done))
		for i := range done {
			indexes = append(indexes, i)
		}
		sort.Slice(indexes, func(a, b int) bool { return indexes[a] < indexes[b] })
		output := make([]any, 0, len(indexes))
		for _, i := range indexes {
			output = append(output, done[i])
		}
		response["output"] = output
	}
	return mustMarshal(response)
}

func mergeGeminiPart(parts []map[string]any, p map[string]any) []map[string]any {
	text, isText := p["text"].(string)
	if !isText || len(parts) == 0 {
		return append(parts, p)
	}
	prev := parts[len(parts)-1]
	prevText, prevIsText := prev["text"].(string)
	if !prevIsText || prev["thought"] != p["thought"] {
		return append(parts, p)
	}
	prev["text"] = prevText + text
	if sig, ok := p["thoughtSignature"]; ok {
		prev["thoughtSignature"] = sig
	}
	return parts
}

func assembleGeminiSSE(body []byte) []byte {
	var parts []map[string]any
	var finish string
	var usage json.RawMessage
	for _, frame := range parseSSE(body) {
		data := gjson.Parse(frame.data)
		for _, part := range data.Get("candidates.0.content.parts").Array() {
			parts = mergeGeminiPart(parts, decodeObject(part.Raw))
		}
		if fr := data.Get("candidates.0.finishReason").String(); fr != "" {
			finish = fr
		}
		if u := data.Get("usageMetadata"); u.IsObject() {
			usage = json.RawMessage(u.Raw)
		}
	}
	candidate := map[string]any{"content": map[string]any{"role": "model", "parts": parts}, "finishReason": finish, "index": 0}
	out := map[string]any{"candidates": []any{candidate}}
	if usage != nil {
		out["usageMetadata"] = usage
	}
	return mustMarshal(out)
}
