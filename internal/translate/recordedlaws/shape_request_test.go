package recordedlaws_test

import (
	"github.com/tidwall/gjson"
)

func validateRequest(format wireFormat, body []byte) []string {
	doc := gjson.ParseBytes(body)
	switch format {
	case anthropicReq:
		return validateAnthropicRequest(doc)
	case chatReq:
		return validateChatRequest(doc)
	case responsesReq:
		return validateResponsesRequest(doc)
	case geminiReq:
		return validateGeminiRequest(doc)
	default:
		return []string{"no request validator for " + string(format)}
	}
}

type pairing struct {
	p       *problems
	pending map[string]bool
}

func newPairing(p *problems) *pairing { return &pairing{p: p, pending: map[string]bool{}} }

func (pr *pairing) call(where, id string) {
	pr.p.require(id != "", "%s tool call has no id", where)
	pr.pending[id] = true
}

func (pr *pairing) result(where, id string) {
	pr.p.require(pr.pending[id], "%s answers %q, which no earlier tool call issued", where, id)
	delete(pr.pending, id)
}

func validateAnthropicRequest(body gjson.Result) []string {
	var p problems
	pr := newPairing(&p)
	msgs := body.Get("messages").Array()
	p.require(len(msgs) > 0 && msgs[0].Get("role").String() == "user", "messages must start with a user turn")
	for i, msg := range msgs {
		role := msg.Get("role").String()
		p.require(role == "user" || role == "assistant", "messages[%d] role %q", i, role)
		p.require(msg.Get("content").Type == gjson.String || len(msg.Get("content").Array()) > 0, "messages[%d] has empty content", i)
		for _, b := range msg.Get("content").Array() {
			switch b.Get("type").String() {
			case "tool_use":
				p.require(anthropicToolUseID.MatchString(b.Get("id").String()), "messages[%d] tool_use id %q is outside ^[a-zA-Z0-9_-]+$", i, b.Get("id").String())
				pr.call("tool_use", b.Get("id").String())
			case "tool_result":
				pr.result("tool_result", b.Get("tool_use_id").String())
			}
		}
	}
	for _, tool := range body.Get("tools").Array() {
		p.require(tool.Get("name").String() != "" && tool.Get("input_schema.type").String() == "object", "tool %q lacks a name or an object input_schema", tool.Get("name").String())
	}
	return p
}

func validateChatRequest(body gjson.Result) []string {
	var p problems
	pr := newPairing(&p)
	for i, msg := range body.Get("messages").Array() {
		role := msg.Get("role").String()
		p.require(map[string]bool{"system": true, "developer": true, "user": true, "assistant": true, "tool": true}[role], "messages[%d] role %q", i, role)
		for j, tc := range msg.Get("tool_calls").Array() {
			validateChatToolCall(&p, j, tc)
			pr.call("assistant", tc.Get("id").String())
		}
		if role == "tool" {
			pr.result("tool message", msg.Get("tool_call_id").String())
		}
	}
	p.require(len(pr.pending) == 0, "tool calls %v are never answered", pr.pending)
	for _, tool := range body.Get("tools").Array() {
		p.require(tool.Get("type").String() == "function" && tool.Get("function.name").String() != "" && tool.Get("function.parameters").IsObject(), "tool %q is not a function tool with object parameters", tool.Get("function.name").String())
	}
	return p
}

func validateClientReplay(format wireFormat, body []byte) []string {
	if format == responsesReq {
		return validateResponsesInput(gjson.ParseBytes(body), withoutThoughtCarrier)
	}
	return validateRequest(format, body)
}

func validateResponsesRequest(body gjson.Result) []string {
	return validateResponsesInput(body, func(id string) string { return id })
}

func validateResponsesInput(body gjson.Result, boundedCallID func(string) string) []string {
	var p problems
	pr := newPairing(&p)
	for _, item := range body.Get("input").Array() {
		switch item.Get("type").String() {
		case "function_call":
			p.require(len(boundedCallID(item.Get("call_id").String())) <= 64, "call_id %q is longer than 64 characters", item.Get("call_id").String())
			pr.call("function_call", item.Get("call_id").String())
		case "function_call_output":
			pr.result("function_call_output", item.Get("call_id").String())
		}
	}
	for _, tool := range body.Get("tools").Array() {
		p = append(p, validateResponsesTool(tool)...)
	}
	return p
}

func validateGeminiRequest(body gjson.Result) []string {
	var p problems
	called := map[string]bool{}
	for i, content := range body.Get("contents").Array() {
		role := content.Get("role").String()
		p.require(role == "user" || role == "model", "contents[%d] role %q", i, role)
		p.require(len(content.Get("parts").Array()) > 0, "contents[%d] has no parts", i)
		validateGeminiParts(&p, "contents", content.Get("parts"))
		for _, part := range content.Get("parts").Array() {
			if name := part.Get("functionCall.name").String(); name != "" {
				called[name] = true
			}
			if fr := part.Get("functionResponse"); fr.Exists() {
				p.require(called[fr.Get("name").String()], "functionResponse %q has no earlier functionCall", fr.Get("name").String())
			}
		}
	}
	return p
}
