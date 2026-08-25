package translate

import (
	"bufio"
	"bytes"
	"strings"

	"github.com/tidwall/gjson"
)

// AnthropicClientResponseText extracts assistant text and tool-call markers
// from the client-facing Anthropic JSON or SSE response shape.
func AnthropicClientResponseText(body []byte) string {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return ""
	}
	if gjson.ValidBytes(trimmed) {
		root := gjson.ParseBytes(trimmed)
		if root.IsObject() {
			return cleanAnthropicResponseText(anthropicResponseContentText(root.Get("content")))
		}
	}

	var out strings.Builder
	scanner := bufio.NewScanner(bytes.NewReader(trimmed))
	scanner.Buffer(make([]byte, 64*1024), len(trimmed)+1)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" || !gjson.Valid(data) {
			continue
		}
		event := gjson.Parse(data)
		if text := event.Get("delta.text"); text.Type == gjson.String {
			out.WriteString(text.String())
		}
		block := event.Get("content_block")
		switch block.Get("type").String() {
		case "text":
			out.WriteString(block.Get("text").String())
		case "tool_use":
			appendToolCallMarker(&out, block.Get("name").String())
		}
	}
	return cleanAnthropicResponseText(out.String())
}

func anthropicResponseContentText(content gjson.Result) string {
	if !content.IsArray() {
		return ""
	}
	var parts []string
	content.ForEach(func(_, block gjson.Result) bool {
		switch block.Get("type").String() {
		case "text":
			if text := block.Get("text").String(); text != "" {
				parts = append(parts, text)
			}
		case "tool_use":
			name := strings.TrimSpace(block.Get("name").String())
			if name == "" {
				name = "tool"
			}
			parts = append(parts, "[tool call] "+name)
		}
		return true
	})
	return strings.Join(parts, "\n")
}

func appendToolCallMarker(out *strings.Builder, name string) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "tool"
	}
	out.WriteString("[tool call] ")
	out.WriteString(name)
}

func cleanAnthropicResponseText(text string) string {
	return strings.TrimSpace(routingMarkerPattern.ReplaceAllString(text, ""))
}
