package recordedlaws_test

import (
	"fmt"
	"sort"
	"strings"

	"github.com/tidwall/gjson"
)

const (
	anthropicReq wireFormat = "anthropic_req"
	chatReq      wireFormat = "chat_req"
	responsesReq wireFormat = "responses_req"
	geminiReq    wireFormat = "gemini_req"
)

type toolResult struct {
	id, content string
}

type requestMessage struct {
	role    string
	turn    turn
	results []toolResult
}

type conversation struct {
	system   []string
	tools    facts
	messages []requestMessage
}

func newConversation() *conversation { return &conversation{tools: facts{}} }

func (c *conversation) message(role string) *requestMessage {
	if n := len(c.messages); n > 0 && c.messages[n-1].role == role {
		return &c.messages[n-1]
	}
	c.messages = append(c.messages, requestMessage{role: role})
	return &c.messages[len(c.messages)-1]
}

func (m *requestMessage) addText(text string) {
	if text == "" {
		return
	}
	if m.turn.text != "" {
		m.turn.text += "\n"
	}
	m.turn.text += text
}

func (c *conversation) addTool(name, description string, schema gjson.Result) {
	c.tools.set("tool."+name+".description", description)
	var props, required []string
	schema.Get("properties").ForEach(func(k, _ gjson.Result) bool {
		props = append(props, k.String())
		return true
	})
	for _, r := range schema.Get("required").Array() {
		required = append(required, r.String())
	}
	sort.Strings(props)
	sort.Strings(required)
	c.tools["tool."+name+".properties"] = strings.Join(props, ",")
	c.tools["tool."+name+".required"] = strings.Join(required, ",")
	c.tools["tool."+name+".schema"] = canonicalJSON(schema.Raw)
}

func (c *conversation) facts() facts {
	f := facts{}
	f.set("system", strings.Join(c.system, "\n"))
	for k, v := range c.tools {
		f[k] = v
	}
	for i, m := range c.messages {
		for k, v := range messageFacts(fmt.Sprintf("msg.%d.", i), m) {
			f[k] = v
		}
	}
	return f
}

func messageFacts(prefix string, m requestMessage) facts {
	f := facts{prefix + "role": m.role}
	body := m.turn
	body.stop, body.usage = "", nil
	for k, v := range body.facts() {
		f[prefix+k] = v
	}
	for i, r := range m.results {
		f.set(fmt.Sprintf("%sresult.%d.id", prefix, i), r.id)
		f.set(fmt.Sprintf("%sresult.%d.content", prefix, i), r.content)
	}
	return f
}

func joinedText(content gjson.Result, kinds ...string) string {
	if content.Type == gjson.String {
		return content.String()
	}
	var parts []string
	for _, part := range content.Array() {
		for _, k := range kinds {
			if part.Get("type").String() == k {
				parts = append(parts, part.Get("text").String())
			}
		}
	}
	return strings.Join(parts, "\n")
}

func (c *conversation) addAnthropicBlock(m *requestMessage, b gjson.Result) {
	switch kind := b.Get("type").String(); kind {
	case "text":
		m.addText(b.Get("text").String())
	case "thinking":
		m.turn.thoughts = append(m.turn.thoughts, thought{text: b.Get("thinking").String(), signature: b.Get("signature").String()})
	case "redacted_thinking":
		m.turn.thoughts = append(m.turn.thoughts, thought{signature: b.Get("data").String(), redacted: true})
	case "tool_use":
		m.turn.tools = append(m.turn.tools, newToolCall(b.Get("id").String(), b.Get("name").String(), b.Get("input").Raw, b.Get("thought_signature").String()))
	case "tool_result":
		m.results = append(m.results, toolResult{id: b.Get("tool_use_id").String(), content: joinedText(b.Get("content"), "text")})
	default:
		m.turn.unknown = append(m.turn.unknown, "anthropic_block_"+kind)
	}
}

func anthropicConversation(body gjson.Result) *conversation {
	c := newConversation()
	if s := joinedText(body.Get("system"), "text"); s != "" {
		c.system = append(c.system, s)
	}
	for _, tool := range body.Get("tools").Array() {
		c.addTool(tool.Get("name").String(), tool.Get("description").String(), tool.Get("input_schema"))
	}
	for _, msg := range body.Get("messages").Array() {
		m := c.message(msg.Get("role").String())
		if content := msg.Get("content"); content.Type == gjson.String {
			m.addText(content.String())
			continue
		}
		for _, b := range msg.Get("content").Array() {
			c.addAnthropicBlock(m, b)
		}
	}
	return c
}

func chatConversation(body gjson.Result) *conversation {
	c := newConversation()
	for _, tool := range body.Get("tools").Array() {
		c.addTool(tool.Get("function.name").String(), tool.Get("function.description").String(), tool.Get("function.parameters"))
	}
	for _, msg := range body.Get("messages").Array() {
		content := joinedText(msg.Get("content"), "text")
		switch role := msg.Get("role").String(); role {
		case "system", "developer":
			c.system = append(c.system, content)
		case "tool":
			m := c.message("user")
			m.results = append(m.results, toolResult{id: msg.Get("tool_call_id").String(), content: content})
		default:
			m := c.message(role)
			m.addText(content)
			if r := msg.Get("reasoning_content").String(); r != "" {
				m.turn.thoughts = append(m.turn.thoughts, thought{text: r})
			}
			for _, tc := range msg.Get("tool_calls").Array() {
				m.turn.tools = append(m.turn.tools, newToolCall(tc.Get("id").String(), tc.Get("function.name").String(), tc.Get("function.arguments").String(), tc.Get("extra_content.google.thought_signature").String()))
			}
		}
	}
	return c
}

func (c *conversation) addResponsesItem(item gjson.Result) {
	kind := item.Get("type").String()
	if kind == "" && item.Get("role").Exists() {
		kind = "message"
	}
	switch kind {
	case "message":
		text := joinedText(item.Get("content"), "input_text", "output_text", "text")
		if role := item.Get("role").String(); role == "system" || role == "developer" {
			c.system = append(c.system, text)
		} else {
			c.message(role).addText(text)
		}
	case "function_call":
		m := c.message("assistant")
		m.turn.tools = append(m.turn.tools, newToolCall(item.Get("call_id").String(), item.Get("name").String(), item.Get("arguments").String(), ""))
	case "function_call_output":
		m := c.message("user")
		m.results = append(m.results, toolResult{id: item.Get("call_id").String(), content: joinedText(item.Get("output"), "input_text", "output_text", "text")})
	case "reasoning":
		m := c.message("assistant")
		m.turn.thoughts = append(m.turn.thoughts, thought{text: joinedText(item.Get("summary"), "summary_text"), signature: item.Get("encrypted_content").String()})
	default:
		c.message("unknown").turn.unknown = append(c.message("unknown").turn.unknown, "responses_item_"+kind)
	}
}

func responsesConversation(body gjson.Result) *conversation {
	c := newConversation()
	if s := body.Get("instructions").String(); s != "" {
		c.system = append(c.system, s)
	}
	for _, tool := range body.Get("tools").Array() {
		c.addTool(tool.Get("name").String(), tool.Get("description").String(), tool.Get("parameters"))
	}
	if input := body.Get("input"); input.Type == gjson.String {
		c.message("user").addText(input.String())
	}
	for _, item := range body.Get("input").Array() {
		c.addResponsesItem(item)
	}
	return c
}

func geminiResultContent(response gjson.Result) string {
	for _, k := range []string{"result", "content", "output"} {
		if v := response.Get(k); v.Type == gjson.String {
			return v.String()
		}
	}
	return canonicalJSON(response.Raw)
}

func (c *conversation) addGeminiPart(m *requestMessage, part gjson.Result) {
	sig := part.Get("thoughtSignature").String()
	switch {
	case part.Get("functionCall").Exists():
		fc := part.Get("functionCall")
		m.turn.tools = append(m.turn.tools, newToolCall(fc.Get("id").String(), fc.Get("name").String(), fc.Get("args").Raw, sig))
	case part.Get("functionResponse").Exists():
		fr := part.Get("functionResponse")
		m.results = append(m.results, toolResult{id: fr.Get("id").String(), content: geminiResultContent(fr.Get("response"))})
	case part.Get("thought").Bool():
		m.turn.thoughts = append(m.turn.thoughts, thought{text: part.Get("text").String(), signature: sig})
	case part.Get("text").Exists():
		m.addText(part.Get("text").String())
	default:
		m.turn.unknown = append(m.turn.unknown, "gemini_part")
	}
}

func geminiConversation(body gjson.Result) *conversation {
	c := newConversation()
	if s := joinedText(body.Get("systemInstruction.parts"), ""); s != "" {
		c.system = append(c.system, s)
	}
	for _, tool := range body.Get("tools").Array() {
		for _, fn := range tool.Get("functionDeclarations").Array() {
			schema := fn.Get("parametersJsonSchema")
			if !schema.Exists() {
				schema = fn.Get("parameters")
			}
			c.addTool(fn.Get("name").String(), fn.Get("description").String(), schema)
		}
	}
	for _, content := range body.Get("contents").Array() {
		role := "user"
		if content.Get("role").String() == "model" {
			role = "assistant"
		}
		m := c.message(role)
		for _, part := range content.Get("parts").Array() {
			c.addGeminiPart(m, part)
		}
	}
	return c
}

func requestConversation(format wireFormat, body []byte) *conversation {
	doc := gjson.ParseBytes(body)
	switch format {
	case anthropicReq:
		return anthropicConversation(doc)
	case chatReq:
		return chatConversation(doc)
	case responsesReq:
		return responsesConversation(doc)
	case geminiReq:
		return geminiConversation(doc)
	default:
		panic("unknown request format " + string(format))
	}
}

func requestFacts(format wireFormat, body []byte) facts {
	return requestConversation(format, body).facts()
}
