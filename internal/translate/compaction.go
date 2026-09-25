package translate

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var ErrUnsafeCompactionBoundary = errors.New("no safe compaction boundary")

type geminiSummaryImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type geminiSummaryBlock struct {
	Type   string                    `json:"type"`
	Text   string                    `json:"text,omitempty"`
	Source *geminiSummaryImageSource `json:"source,omitempty"`
}

func (e *RequestEnvelope) GeminiCompactionSummaryBody() ([]byte, error) {
	if e == nil || e.format != FormatGemini {
		return nil, fmt.Errorf("gemini summary requires a Gemini request")
	}
	type message struct {
		Role    string               `json:"role"`
		Content []geminiSummaryBlock `json:"content"`
	}
	projection := struct {
		System   string    `json:"system,omitempty"`
		Messages []message `json:"messages"`
	}{Messages: make([]message, 0)}
	system := gjson.GetBytes(e.body, "systemInstruction")
	if system.Exists() {
		for _, part := range system.Get("parts").Array() {
			if !part.IsObject() || len(part.Map()) != 1 || !part.Get("text").Exists() {
				return nil, fmt.Errorf("unsupported Gemini system instruction part")
			}
			projection.System += part.Get("text").String() + "\n"
		}
		if projection.System == "" {
			return nil, fmt.Errorf("empty Gemini system instruction")
		}
	}
	for _, content := range gjson.GetBytes(e.body, "contents").Array() {
		role := "user"
		if content.Get("role").String() == "model" {
			role = "assistant"
		} else if r := content.Get("role").String(); r != "" && r != "user" {
			return nil, fmt.Errorf("unsupported Gemini role %q", r)
		}
		out := message{Role: role}
		for _, part := range content.Get("parts").Array() {
			contentFields := 0
			for key := range part.Map() {
				switch key {
				case "text", "functionCall", "functionResponse", "inlineData", "thoughtSignature", "thought":
					if key != "thoughtSignature" && key != "thought" {
						contentFields++
					}
				default:
					return nil, fmt.Errorf("unsupported Gemini summary part %q", key)
				}
			}
			if contentFields != 1 {
				return nil, fmt.Errorf("Gemini summary part has %d content fields", contentFields)
			}
			switch {
			case part.Get("text").Exists():
				out.Content = append(out.Content, geminiSummaryBlock{Type: "text", Text: part.Get("text").String()})
			case part.Get("functionCall").Exists():
				call := part.Get("functionCall")
				if call.Get("name").String() == "" {
					return nil, fmt.Errorf("Gemini function call missing name")
				}
				args := call.Get("args").Raw
				if args == "" {
					args = "{}"
				}
				out.Content = append(out.Content, geminiSummaryBlock{Type: "text", Text: fmt.Sprintf("Function call %s: %s", call.Get("name").String(), args)})
			case part.Get("functionResponse").Exists():
				response := part.Get("functionResponse")
				if response.Get("name").String() == "" || !response.Get("response").Exists() {
					return nil, fmt.Errorf("Gemini function response missing name or body")
				}
				out.Content = append(out.Content, geminiSummaryBlock{Type: "text", Text: fmt.Sprintf("Function response %s: %s", response.Get("name").String(), response.Get("response").Raw)})
			case part.Get("inlineData").Exists():
				image := part.Get("inlineData")
				mime := image.Get("mimeType").String()
				switch mime {
				case "image/jpeg", "image/png", "image/gif", "image/webp":
				default:
					return nil, fmt.Errorf("unsupported Gemini summary media type %q", mime)
				}
				if image.Get("data").String() == "" {
					return nil, fmt.Errorf("Gemini image missing data")
				}
				out.Content = append(out.Content, geminiSummaryBlock{Type: "image", Source: &geminiSummaryImageSource{Type: "base64", MediaType: mime, Data: image.Get("data").String()}})
			default:
				return nil, fmt.Errorf("Gemini summary part has no supported content")
			}
		}
		if len(out.Content) == 0 {
			return nil, fmt.Errorf("Gemini summary message is empty")
		}
		projection.Messages = append(projection.Messages, out)
	}
	if len(projection.Messages) == 0 {
		return nil, fmt.Errorf("Gemini summary has no messages")
	}
	return json.Marshal(projection)
}

func (e *RequestEnvelope) SupportsHistoryCompaction() bool {
	if e == nil {
		return false
	}
	if e.format != FormatOpenAI {
		return true
	}
	seenConversation := false
	for _, message := range gjson.GetBytes(e.body, "messages").Array() {
		switch message.Get("role").String() {
		case "system", "developer":
			if seenConversation {
				return false
			}
		default:
			seenConversation = true
		}
	}
	return true
}

func (e *RequestEnvelope) CompactionTailBoundary(keepRecent int) int {
	boundaries := e.CompactionBoundaries()
	if len(boundaries) < 2 {
		return 0
	}
	target := max(boundaries[len(boundaries)-1]-keepRecent, 0)
	boundary := 0
	for _, candidate := range boundaries[1 : len(boundaries)-1] {
		if candidate > target {
			break
		}
		boundary = candidate
	}
	return boundary
}

func (e *RequestEnvelope) CompactionPrefixDigest(boundary int) ([sha256.Size]byte, error) {
	var zero [sha256.Size]byte
	boundaries := e.CompactionBoundaries()
	valid := false
	for _, b := range boundaries {
		valid = valid || b == boundary
	}
	if !valid || e == nil {
		return zero, ErrUnsafeCompactionBoundary
	}
	field := "messages"
	if e.format == FormatGemini {
		field = "contents"
	}
	all := gjson.GetBytes(e.body, field).Array()
	if boundary >= len(all) {
		return zero, ErrUnsafeCompactionBoundary
	}
	prefix := make([]json.RawMessage, boundary)
	for i, message := range all[:boundary] {
		prefix[i] = json.RawMessage(message.Raw)
	}
	relevant, err := json.Marshal(struct {
		Format       Format            `json:"format"`
		System       string            `json:"system"`
		Instructions string            `json:"instructions"`
		Prefix       []json.RawMessage `json:"prefix"`
	}{
		Format:       e.format,
		System:       gjson.GetBytes(e.body, "system").Raw,
		Instructions: gjson.GetBytes(e.body, "instructions").Raw,
		Prefix:       prefix,
	})
	if err != nil {
		return zero, err
	}
	return sha256.Sum256(relevant), nil
}

func (e *RequestEnvelope) CompactionBoundaries() []int {
	if e == nil {
		return nil
	}
	field := "messages"
	if e.format == FormatGemini {
		field = "contents"
	}
	all := gjson.GetBytes(e.body, field).Array()
	boundaries := []int{0}
	for i := 1; i < len(all); i++ {
		if e.format == FormatOpenAI && (all[i].Get("role").String() == "system" || all[i].Get("role").String() == "developer") {
			continue
		}
		if isTextBearingUserMessage(all[i], e.format) && !containsToolResult(all[i], e.format) {
			boundaries = append(boundaries, i)
		} else if isAssistantMessage(all[i], e.format) && containsToolResult(all[i-1], e.format) {
			boundaries = append(boundaries, i)
		}
	}
	if len(all) > 0 {
		boundaries = append(boundaries, len(all))
	}
	return boundaries
}

func containsToolResult(message gjson.Result, format Format) bool {
	switch format {
	case FormatAnthropic:
		for _, b := range message.Get("content").Array() {
			if b.Get("type").String() == "tool_result" {
				return true
			}
		}
	case FormatOpenAI:
		return message.Get("role").String() == "tool"
	case FormatGemini:
		for _, p := range message.Get("parts").Array() {
			if p.Get("functionResponse").Exists() {
				return true
			}
		}
	}
	return false
}

func (e *RequestEnvelope) CompactionChunk(start, end int, summary string) (*RequestEnvelope, error) {
	if !e.SupportsHistoryCompaction() {
		return nil, ErrUnsafeCompactionBoundary
	}
	field := "messages"
	if e.format == FormatGemini {
		field = "contents"
	}
	all := gjson.GetBytes(e.body, field).Array()
	boundaries := e.CompactionBoundaries()
	validStart, validEnd := false, false
	for _, boundary := range boundaries {
		validStart = validStart || boundary == start
		validEnd = validEnd || boundary == end
	}
	if !validStart || !validEnd || end <= start {
		return nil, ErrUnsafeCompactionBoundary
	}
	rebuilt := make([]string, 0, end-start+3)
	if e.format == FormatOpenAI && start > 0 {
		for _, msg := range all[:start] {
			if role := msg.Get("role").String(); role == "system" || role == "developer" {
				rebuilt = append(rebuilt, msg.Raw)
			}
		}
	}
	if summary != "" {
		switch e.format {
		case FormatAnthropic:
			rebuilt = append(rebuilt, anthropicAssistantSummaryBlock(summary))
		case FormatOpenAI:
			rebuilt = append(rebuilt, openAIAssistantSummaryMessage(summary))
		case FormatGemini:
			raw, _ := json.Marshal(map[string]any{"role": "model", "parts": []any{map[string]any{"text": HandoverSummaryTag + summary}}})
			rebuilt = append(rebuilt, string(raw))
		}
	}
	if isAssistantMessage(all[start], e.format) {
		switch e.format {
		case FormatAnthropic:
			rebuilt = append(rebuilt, `{"role":"user","content":"Continue from the preceding conversation summary."}`)
		case FormatOpenAI:
			rebuilt = append(rebuilt, `{"role":"user","content":"Continue from the preceding conversation summary."}`)
		case FormatGemini:
			rebuilt = append(rebuilt, `{"role":"user","parts":[{"text":"Continue from the preceding conversation summary."}]}`)
		}
	}
	for _, msg := range all[start:end] {
		rebuilt = append(rebuilt, msg.Raw)
	}
	chunk := e.Clone()
	var err error
	chunk.body, err = sjson.SetRawBytes(chunk.body, field, []byte("["+strings.Join(rebuilt, ",")+"]"))
	if err != nil {
		return nil, err
	}
	return chunk, nil
}

// ClearedToolResultPlaceholder replaces the body of an old tool result during
// Tier-1 compaction cleanup. Mirrors Claude Code's own placeholder so a reader
// (human or model) recognizes that the content was elided, not lost.
const ClearedToolResultPlaceholder = "[Old tool result content cleared]"

// ClearOldToolResults replaces every tool result except the most recent
// keepRecent with ClearedToolResultPlaceholder, leaving structure intact and
// returning the number cleared. Tool results dominate agentic-session tokens;
// clearing stale ones is the cheap, model-free Tier-1 step that often avoids a
// full summarization. Pure: no I/O. keepRecent < 0 is treated as 0.
func (e *RequestEnvelope) ClearOldToolResults(keepRecent int) int {
	if e == nil {
		return 0
	}
	keepRecent = max(keepRecent, 0)
	switch e.format {
	case FormatAnthropic:
		return e.clearOldToolResultsAnthropic(keepRecent)
	case FormatOpenAI:
		return e.clearOldToolResultsOpenAI(keepRecent)
	case FormatGemini:
		return e.clearOldToolResultsGemini(keepRecent)
	default:
		return 0
	}
}

// RewriteForCompaction rewrites history to [summary + recent keepRecentTurns
// non-system messages], aligned to a text-bearing user turn or the start of
// a complete assistant/tool-result batch. Orphaned tool results (whose tool
// use was elided) are stripped to keep the request wire-valid. Unlike
// RewriteForHandover, a tail is kept so the model retains immediate working
// context. Returns the number of messages elided. Pure: no I/O.
// keepRecentTurns <= 0 is treated as 1.
func (e *RequestEnvelope) RewriteForCompaction(summary string, keepRecentTurns int) int {
	if !e.SupportsHistoryCompaction() {
		return 0
	}
	keepRecentTurns = max(keepRecentTurns, 1)
	switch e.format {
	case FormatAnthropic:
		return e.rewriteAnthropicForCompaction(summary, keepRecentTurns)
	case FormatOpenAI:
		return e.rewriteOpenAIForCompaction(summary, keepRecentTurns)
	case FormatGemini:
		return e.rewriteGeminiForCompaction(summary, keepRecentTurns)
	default:
		return 0
	}
}

// userTextAlignedStart returns the index at which to begin a recent-message
// window, advanced to a user turn that still carries request text. Tool-result
// turns do not establish a usable routing boundary.
func userTextAlignedStart(msgs []gjson.Result, keepRecent int, format Format) int {
	start := max(len(msgs)-keepRecent, 0)
	for start < len(msgs) && !isTextBearingUserMessage(msgs[start], format) {
		start++
	}
	if start >= len(msgs) {
		for i := len(msgs) - 1; i >= 0; i-- {
			if isTextBearingUserMessage(msgs[i], format) {
				return i
			}
		}
		return len(msgs)
	}
	return start
}

func compactionTailStart(msgs []gjson.Result, keepRecent int, format Format) int {
	start := userTextAlignedStart(msgs, keepRecent, format)
	target := max(len(msgs)-keepRecent, 0)
	if start >= target {
		return start
	}
	if containsToolResult(msgs[start], format) {
		return start
	}
	for i := target; i < len(msgs); i++ {
		if i > 0 && isAssistantMessage(msgs[i], format) && containsToolResult(msgs[i-1], format) {
			return i
		}
	}
	for i := target - 1; i > start; i-- {
		if isAssistantMessage(msgs[i], format) && containsToolResult(msgs[i-1], format) {
			return i
		}
	}
	return start
}

func compactionUserAnchor(msgs []gjson.Result, start int, format Format) string {
	if start == 0 || start >= len(msgs) || !isAssistantMessage(msgs[start], format) {
		return ""
	}
	for i := start - 1; i >= 0; i-- {
		if isTextBearingUserMessage(msgs[i], format) {
			if !containsToolResult(msgs[i], format) {
				return msgs[i].Raw
			}
			return ""
		}
	}
	return ""
}

func (e *RequestEnvelope) clearOldToolResultsAnthropic(keepRecent int) int {
	msgs := gjson.GetBytes(e.body, "messages")
	if !msgs.IsArray() {
		return 0
	}
	all := msgs.Array()

	total := 0
	for _, m := range all {
		if m.Get("role").String() != "user" {
			continue
		}
		m.Get("content").ForEach(func(_, b gjson.Result) bool {
			if b.Get("type").String() == "tool_result" {
				total++
			}
			return true
		})
	}
	cutoff := total - keepRecent
	if cutoff <= 0 {
		return 0
	}

	seen, cleared := 0, 0
	rebuilt := make([]string, 0, len(all))
	for _, m := range all {
		content := m.Get("content")
		if m.Get("role").String() != "user" || !content.IsArray() {
			rebuilt = append(rebuilt, m.Raw)
			continue
		}
		newBlocks := make([]string, 0, len(content.Array()))
		changed := false
		content.ForEach(func(_, b gjson.Result) bool {
			if b.Get("type").String() == "tool_result" {
				seen++
				if seen <= cutoff {
					nb, err := sjson.SetBytes([]byte(b.Raw), "content", ClearedToolResultPlaceholder)
					if err == nil {
						newBlocks = append(newBlocks, string(nb))
						cleared++
						changed = true
						return true
					}
				}
			}
			newBlocks = append(newBlocks, b.Raw)
			return true
		})
		if !changed {
			rebuilt = append(rebuilt, m.Raw)
			continue
		}
		newContent := "[" + strings.Join(newBlocks, ",") + "]"
		nm, err := sjson.SetRawBytes([]byte(m.Raw), "content", []byte(newContent))
		if err != nil {
			rebuilt = append(rebuilt, m.Raw)
			continue
		}
		rebuilt = append(rebuilt, string(nm))
	}
	if cleared == 0 {
		return 0
	}
	return e.setMessages(rebuilt, cleared)
}

func (e *RequestEnvelope) clearOldToolResultsOpenAI(keepRecent int) int {
	msgs := gjson.GetBytes(e.body, "messages")
	if !msgs.IsArray() {
		return 0
	}
	all := msgs.Array()

	total := 0
	for _, m := range all {
		if m.Get("role").String() == "tool" {
			total++
		}
	}
	cutoff := total - keepRecent
	if cutoff <= 0 {
		return 0
	}

	seen, cleared := 0, 0
	rebuilt := make([]string, 0, len(all))
	for _, m := range all {
		if m.Get("role").String() != "tool" {
			rebuilt = append(rebuilt, m.Raw)
			continue
		}
		seen++
		if seen <= cutoff {
			nm, err := sjson.SetBytes([]byte(m.Raw), "content", ClearedToolResultPlaceholder)
			if err == nil {
				rebuilt = append(rebuilt, string(nm))
				cleared++
				continue
			}
		}
		rebuilt = append(rebuilt, m.Raw)
	}
	if cleared == 0 {
		return 0
	}
	return e.setMessages(rebuilt, cleared)
}

func (e *RequestEnvelope) clearOldToolResultsGemini(keepRecent int) int {
	contents := gjson.GetBytes(e.body, "contents")
	if !contents.IsArray() {
		return 0
	}
	all := contents.Array()

	total := 0
	for _, c := range all {
		c.Get("parts").ForEach(func(_, p gjson.Result) bool {
			if p.Get("functionResponse").Exists() {
				total++
			}
			return true
		})
	}
	cutoff := total - keepRecent
	if cutoff <= 0 {
		return 0
	}

	seen, cleared := 0, 0
	rebuilt := make([]string, 0, len(all))
	for _, c := range all {
		parts := c.Get("parts")
		if !parts.IsArray() {
			rebuilt = append(rebuilt, c.Raw)
			continue
		}
		newParts := make([]string, 0, len(parts.Array()))
		changed := false
		parts.ForEach(func(_, p gjson.Result) bool {
			if p.Get("functionResponse").Exists() {
				seen++
				if seen <= cutoff {
					np, err := sjson.SetBytes([]byte(p.Raw), "functionResponse.response", map[string]any{"result": ClearedToolResultPlaceholder})
					if err == nil {
						newParts = append(newParts, string(np))
						cleared++
						changed = true
						return true
					}
				}
			}
			newParts = append(newParts, p.Raw)
			return true
		})
		if !changed {
			rebuilt = append(rebuilt, c.Raw)
			continue
		}
		newPartsRaw := "[" + strings.Join(newParts, ",") + "]"
		nc, err := sjson.SetRawBytes([]byte(c.Raw), "parts", []byte(newPartsRaw))
		if err != nil {
			rebuilt = append(rebuilt, c.Raw)
			continue
		}
		rebuilt = append(rebuilt, string(nc))
	}
	if cleared == 0 {
		return 0
	}
	out, err := sjson.SetRawBytes(e.body, "contents", []byte("["+strings.Join(rebuilt, ",")+"]"))
	if err != nil {
		return 0
	}
	e.body = out
	return cleared
}

// setMessages writes rebuilt back to the "messages" array and returns ret on
// success, 0 on marshal failure. Shared by the Anthropic/OpenAI message-array
// rewriters.
func (e *RequestEnvelope) setMessages(rebuilt []string, ret int) int {
	out, err := sjson.SetRawBytes(e.body, "messages", []byte("["+strings.Join(rebuilt, ",")+"]"))
	if err != nil {
		return 0
	}
	e.body = out
	return ret
}

func (e *RequestEnvelope) rewriteAnthropicForCompaction(summary string, keepRecent int) int {
	msgs := gjson.GetBytes(e.body, "messages")
	if !msgs.IsArray() {
		return 0
	}
	all := msgs.Array()
	if len(all) == 0 {
		return 0
	}
	start := compactionTailStart(all, keepRecent, FormatAnthropic)
	cleaned, _ := stripOrphanedAnthropicToolResults(all[start:])
	rebuilt := []string{anthropicAssistantSummaryBlock(summary)}
	if start < len(all) && isAssistantMessage(all[start], FormatAnthropic) {
		anchor := compactionUserAnchor(all, start, FormatAnthropic)
		if anchor == "" {
			anchor = `{"role":"user","content":"Continue from the preceding conversation summary."}`
		}
		rebuilt = append(rebuilt, anchor)
	}
	rebuilt = append(rebuilt, cleaned...)
	elided := max(len(all)-len(cleaned), 0)
	return e.setMessages(rebuilt, elided)
}

func (e *RequestEnvelope) rewriteOpenAIForCompaction(summary string, keepRecent int) int {
	msgs := gjson.GetBytes(e.body, "messages")
	if !msgs.IsArray() {
		return 0
	}
	all := msgs.Array()
	if len(all) == 0 {
		return 0
	}
	systems := make([]string, 0)
	others := make([]gjson.Result, 0, len(all))
	for _, m := range all {
		if m.Get("role").String() == "system" {
			systems = append(systems, m.Raw)
			continue
		}
		others = append(others, m)
	}
	if len(others) == 0 {
		return 0
	}
	start := compactionTailStart(others, keepRecent, FormatOpenAI)
	keptRaw := make([]string, 0, len(others)-start)
	for _, m := range others[start:] {
		keptRaw = append(keptRaw, m.Raw)
	}
	cleaned := stripOrphanedOpenAIToolMessages(keptRaw)
	rebuilt := make([]string, 0, len(systems)+1+len(cleaned))
	rebuilt = append(rebuilt, systems...)
	rebuilt = append(rebuilt, openAIAssistantSummaryMessage(summary))
	if start < len(others) && isAssistantMessage(others[start], FormatOpenAI) {
		anchor := compactionUserAnchor(others, start, FormatOpenAI)
		if anchor == "" {
			anchor = `{"role":"user","content":"Continue from the preceding conversation summary."}`
		}
		rebuilt = append(rebuilt, anchor)
	}
	rebuilt = append(rebuilt, cleaned...)
	elided := max(len(others)-len(cleaned), 0)
	return e.setMessages(rebuilt, elided)
}

func (e *RequestEnvelope) rewriteGeminiForCompaction(summary string, keepRecent int) int {
	contents := gjson.GetBytes(e.body, "contents")
	if !contents.IsArray() {
		return 0
	}
	all := contents.Array()
	if len(all) == 0 {
		return 0
	}
	start := compactionTailStart(all, keepRecent, FormatGemini)
	kept := stripLeadingGeminiOrphanFunctionResponses(all[start:])

	tagged := HandoverSummaryTag + summary
	summaryEntry := map[string]any{
		"role":  "model",
		"parts": []any{map[string]any{"text": tagged}},
	}
	summaryRaw, _ := json.Marshal(summaryEntry)

	rebuilt := make([]string, 0, len(kept)+1)
	rebuilt = append(rebuilt, string(summaryRaw))
	if start < len(all) && isAssistantMessage(all[start], FormatGemini) {
		anchor := compactionUserAnchor(all, start, FormatGemini)
		if anchor == "" {
			anchor = `{"role":"user","parts":[{"text":"Continue from the preceding conversation summary."}]}`
		}
		rebuilt = append(rebuilt, anchor)
	}
	rebuilt = append(rebuilt, kept...)
	elided := max(len(all)-len(kept), 0)
	out, err := sjson.SetRawBytes(e.body, "contents", []byte("["+strings.Join(rebuilt, ",")+"]"))
	if err != nil {
		return 0
	}
	e.body = out
	return elided
}

// stripLeadingGeminiOrphanFunctionResponses drops functionResponse parts from
// the first kept content: Gemini requires a function response turn to
// immediately follow the model's functionCall turn, and the head of a kept
// tail has had that turn elided. Only the head can be orphaned — every later
// functionResponse still has its functionCall in the window. A head left
// with no parts is dropped.
func stripLeadingGeminiOrphanFunctionResponses(kept []gjson.Result) []string {
	out := make([]string, 0, len(kept))
	for i, c := range kept {
		parts := c.Get("parts")
		if i > 0 || !parts.IsArray() {
			out = append(out, c.Raw)
			continue
		}
		keptParts := make([]string, 0, len(parts.Array()))
		dropped := false
		parts.ForEach(func(_, p gjson.Result) bool {
			if p.Get("functionResponse").Exists() {
				dropped = true
				return true
			}
			keptParts = append(keptParts, p.Raw)
			return true
		})
		switch {
		case !dropped:
			out = append(out, c.Raw)
		case len(keptParts) == 0:
		default:
			nc, err := sjson.SetRawBytes([]byte(c.Raw), "parts", []byte("["+strings.Join(keptParts, ",")+"]"))
			if err != nil {
				out = append(out, c.Raw)
				continue
			}
			out = append(out, string(nc))
		}
	}
	return out
}
