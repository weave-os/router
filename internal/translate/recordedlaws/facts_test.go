package recordedlaws_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
)

type facts map[string]string

type toolCall struct {
	id, name, args, signature string
}

type thought struct {
	text, signature string
	redacted        bool
}

type turn struct {
	text     string
	thoughts []thought
	tools    []toolCall
	stop     string
	usage    map[string]int64
	unknown  []string
}

const thoughtCarrier = "__thought__"

func splitThoughtCarrier(id string) (string, string) {
	clean, encoded, ok := strings.Cut(id, thoughtCarrier)
	if !ok {
		return id, ""
	}
	sig, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return id, ""
	}
	return clean, string(sig)
}

func canonicalJSON(raw string) string {
	if strings.TrimSpace(raw) == "" {
		raw = "{}"
	}
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return "invalid-json:" + raw
	}
	return string(mustMarshal(v))
}

func newToolCall(id, name, args, signature string) toolCall {
	clean, carried := splitThoughtCarrier(id)
	if signature == "" {
		signature = carried
	}
	return toolCall{id: clean, name: name, args: canonicalJSON(args), signature: signature}
}

func (t turn) facts() facts {
	f := facts{}
	f.set("text", t.text)
	f.set("stop", t.stop)
	for i, th := range t.thoughts {
		f.set(fmt.Sprintf("thinking.%d.text", i), th.text)
		f.set(fmt.Sprintf("thinking.%d.signature", i), th.signature)
		if th.redacted {
			f.set(fmt.Sprintf("thinking.%d.redacted", i), "true")
		}
	}
	for i, tc := range t.tools {
		f.set(fmt.Sprintf("tool.%d.id", i), tc.id)
		f.set(fmt.Sprintf("tool.%d.name", i), tc.name)
		f.set(fmt.Sprintf("tool.%d.args", i), tc.args)
		f.set(fmt.Sprintf("tool.%d.signature", i), tc.signature)
	}
	for k, v := range t.usage {
		f["usage."+k] = fmt.Sprint(v)
	}
	for _, u := range t.unknown {
		f["unknown."+u] = "present"
	}
	return f
}

func (f facts) set(key, value string) {
	if value != "" {
		f[key] = value
	}
}

func usageFrom(u gjson.Result, fields map[string][]string) map[string]int64 {
	if !u.IsObject() {
		return nil
	}
	out := map[string]int64{}
	for canonical, paths := range fields {
		for _, p := range paths {
			if v := u.Get(p); v.Exists() && v.Type == gjson.Number {
				out[canonical] += v.Int()
			}
		}
	}
	return out
}

var (
	anthropicUsageFields = map[string][]string{"input_total": {"input_tokens", "cache_read_input_tokens", "cache_creation_input_tokens"}, "cache_read": {"cache_read_input_tokens"}, "cache_write": {"cache_creation_input_tokens"}, "output": {"output_tokens"}}
	chatUsageFields      = map[string][]string{"input_total": {"prompt_tokens"}, "cache_read": {"prompt_tokens_details.cached_tokens"}, "cache_write": {"prompt_tokens_details.cache_write_tokens"}, "output": {"completion_tokens"}}
	responsesUsageFields = map[string][]string{"input_total": {"input_tokens"}, "cache_read": {"input_tokens_details.cached_tokens"}, "cache_write": {"input_tokens_details.cache_write_tokens"}, "output": {"output_tokens"}}
	geminiUsageFields    = map[string][]string{"input_total": {"promptTokenCount"}, "cache_read": {"cachedContentTokenCount"}, "output": {"candidatesTokenCount", "thoughtsTokenCount"}}
)

func anthropicTurn(msg gjson.Result) turn {
	var tr turn
	var text strings.Builder
	for _, b := range msg.Get("content").Array() {
		switch kind := b.Get("type").String(); kind {
		case "text":
			text.WriteString(b.Get("text").String())
		case "thinking":
			tr.thoughts = append(tr.thoughts, thought{text: b.Get("thinking").String(), signature: b.Get("signature").String()})
		case "redacted_thinking":
			tr.thoughts = append(tr.thoughts, thought{signature: b.Get("data").String(), redacted: true})
		case "tool_use":
			tr.tools = append(tr.tools, newToolCall(b.Get("id").String(), b.Get("name").String(), b.Get("input").Raw, b.Get("thought_signature").String()))
		default:
			tr.unknown = append(tr.unknown, "anthropic_block_"+kind)
		}
	}
	tr.text = text.String()
	tr.stop = msg.Get("stop_reason").String()
	tr.usage = usageFrom(msg.Get("usage"), anthropicUsageFields)
	return tr
}

var chatStops = map[string]string{"stop": "end_turn", "tool_calls": "tool_use", "length": "max_tokens"}

func chatTurn(body gjson.Result) turn {
	msg := body.Get("choices.0.message")
	tr := turn{text: msg.Get("content").String()}
	if r := msg.Get("reasoning_content").String(); r != "" {
		tr.thoughts = append(tr.thoughts, thought{text: r})
	}
	for _, tc := range msg.Get("tool_calls").Array() {
		tr.tools = append(tr.tools, newToolCall(tc.Get("id").String(), tc.Get("function.name").String(), tc.Get("function.arguments").String(), tc.Get("function.thought_signature").String()))
	}
	tr.stop = mapped(chatStops, "chat", body.Get("choices.0.finish_reason").String())
	tr.usage = usageFrom(body.Get("usage"), chatUsageFields)
	return tr
}

func mapped(table map[string]string, family, value string) string {
	if v, ok := table[value]; ok {
		return v
	}
	if value == "" {
		return ""
	}
	return family + ":" + value
}

func responsesTurn(resp gjson.Result) turn {
	var tr turn
	var text strings.Builder
	for _, item := range resp.Get("output").Array() {
		switch kind := item.Get("type").String(); kind {
		case "message":
			for _, part := range item.Get("content").Array() {
				text.WriteString(part.Get("text").String())
			}
		case "reasoning":
			var summary []string
			for _, s := range item.Get("summary").Array() {
				summary = append(summary, s.Get("text").String())
			}
			tr.thoughts = append(tr.thoughts, thought{text: strings.Join(summary, ""), signature: item.Get("encrypted_content").String()})
		case "function_call":
			tr.tools = append(tr.tools, newToolCall(item.Get("call_id").String(), item.Get("name").String(), item.Get("arguments").String(), ""))
		default:
			tr.unknown = append(tr.unknown, "responses_item_"+kind)
		}
	}
	tr.text = text.String()
	tr.stop = responsesStop(resp, len(tr.tools) > 0)
	tr.usage = usageFrom(resp.Get("usage"), responsesUsageFields)
	return tr
}

func responsesStop(resp gjson.Result, hasTools bool) string {
	switch status := resp.Get("status").String(); {
	case status == "completed" && hasTools:
		return "tool_use"
	case status == "completed":
		return "end_turn"
	case status == "incomplete" && resp.Get("incomplete_details.reason").String() == "max_output_tokens":
		return "max_tokens"
	default:
		return "responses:" + status
	}
}

func geminiTurn(body gjson.Result) turn {
	var tr turn
	var text strings.Builder
	for _, part := range body.Get("candidates.0.content.parts").Array() {
		sig := part.Get("thoughtSignature").String()
		switch {
		case part.Get("functionCall").Exists():
			fc := part.Get("functionCall")
			tr.tools = append(tr.tools, newToolCall(fc.Get("id").String(), fc.Get("name").String(), fc.Get("args").Raw, sig))
		case part.Get("thought").Bool():
			tr.thoughts = append(tr.thoughts, thought{text: part.Get("text").String(), signature: sig})
		case part.Get("text").Exists():
			text.WriteString(part.Get("text").String())
			if sig != "" {
				tr.thoughts = append(tr.thoughts, thought{signature: sig})
			}
		default:
			tr.unknown = append(tr.unknown, "gemini_part")
		}
	}
	tr.text = text.String()
	tr.stop = geminiStop(body.Get("candidates.0.finishReason").String(), len(tr.tools) > 0)
	tr.usage = usageFrom(body.Get("usageMetadata"), geminiUsageFields)
	return tr
}

func geminiStop(reason string, hasTools bool) string {
	switch {
	case reason == "STOP" && hasTools:
		return "tool_use"
	case reason == "STOP":
		return "end_turn"
	case reason == "MAX_TOKENS":
		return "max_tokens"
	default:
		return mapped(nil, "gemini", reason)
	}
}
