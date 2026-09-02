package translate

import (
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// HandoverSummaryTag prefixes the synthesized summary message inserted by
// RewriteForHandover so it's distinguishable from real assistant output.
const HandoverSummaryTag = "[handover summary] "

// RewriteForHandover replaces all non-system messages with [assistantSummary,
// latestUserMessage] to bound input-token cost on mid-session model switches.
// Returns the count of elided messages; no-ops if there are none.
func (e *RequestEnvelope) RewriteForHandover(summary string) int {
	if e == nil {
		return 0
	}
	switch e.format {
	case FormatAnthropic:
		return e.rewriteAnthropicForHandover(summary)
	case FormatOpenAI:
		return e.rewriteOpenAIForHandover(summary)
	case FormatGemini:
		return e.rewriteGeminiForHandover(summary)
	default:
		return 0
	}
}

// TrimLastNMessages keeps the most recent n non-system messages plus system
// blocks. Falls back to n=3 when n <= 0. Returns the number elided.
func (e *RequestEnvelope) TrimLastNMessages(n int) int {
	if e == nil {
		return 0
	}
	if n <= 0 {
		n = 3
	}
	switch e.format {
	case FormatAnthropic:
		return e.trimAnthropicLastN(n)
	case FormatOpenAI:
		return e.trimOpenAILastN(n)
	case FormatGemini:
		return e.trimGeminiLastN(n)
	default:
		return 0
	}
}

// rewriteAnthropicForHandover rewrites the "messages" array for Anthropic format.
func (e *RequestEnvelope) rewriteAnthropicForHandover(summary string) int {
	msgs := gjson.GetBytes(e.body, "messages")
	if !msgs.IsArray() {
		return 0
	}
	all := msgs.Array()
	if len(all) == 0 {
		return 0
	}

	// Find the last user message (walking from the end).
	var latestUser gjson.Result
	for i := len(all) - 1; i >= 0; i-- {
		if all[i].Get("role").String() == "user" {
			latestUser = all[i]
			break
		}
	}

	summaryBlock := anthropicAssistantSummaryBlock(summary)
	rebuilt := []string{summaryBlock}
	preserved := 0
	if latestUser.Exists() {
		// Strip tool_result blocks: the summary has no tool_use blocks,
		// so any tool_results would be orphaned.
		cleaned, _ := stripAnthropicToolResultMsg(latestUser, nil)
		if cleaned != "" {
			rebuilt = append(rebuilt, cleaned)
			preserved = 1
		}
	}

	// elided counts original conversation messages no longer present;
	// the synthesized summary is not part of the original conversation.
	elided := max(len(all)-preserved, 0)

	newMessages := "[" + strings.Join(rebuilt, ",") + "]"
	out, err := sjson.SetRawBytes(e.body, "messages", []byte(newMessages))
	if err != nil {
		return 0
	}
	e.body = out
	return elided
}

// anthropicAssistantSummaryBlock builds a synthesized assistant entry with a
// single text block containing the tagged summary.
func anthropicAssistantSummaryBlock(summary string) string {
	tagged := HandoverSummaryTag + summary
	msg := map[string]any{
		"role": "assistant",
		"content": []any{
			map[string]any{"type": "text", "text": tagged},
		},
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		// json.Marshal can only fail on unsupported values; both keys
		// are strings, so this is defensive.
		escaped, _ := json.Marshal(tagged)
		return `{"role":"assistant","content":[{"type":"text","text":` + string(escaped) + `}]}`
	}
	return string(raw)
}

func (e *RequestEnvelope) trimAnthropicLastN(n int) int {
	msgs := gjson.GetBytes(e.body, "messages")
	if !msgs.IsArray() {
		return 0
	}
	all := msgs.Array()
	if len(all) <= n {
		return 0
	}
	keep := all[len(all)-n:]
	rebuilt, _ := stripOrphanedAnthropicToolResults(keep)
	newMessages := "[" + strings.Join(rebuilt, ",") + "]"
	out, err := sjson.SetRawBytes(e.body, "messages", []byte(newMessages))
	if err != nil {
		return 0
	}
	e.body = out
	return len(all) - n
}

// rewriteOpenAIForHandover preserves role=="system" messages and replaces
// every other message with [assistantSummary, latestUser].
func (e *RequestEnvelope) rewriteOpenAIForHandover(summary string) int {
	msgs := gjson.GetBytes(e.body, "messages")
	if !msgs.IsArray() {
		return 0
	}
	all := msgs.Array()
	if len(all) == 0 {
		return 0
	}

	systems := make([]string, 0)
	others := make([]string, 0, len(all))
	for _, m := range all {
		if m.Get("role").String() == "system" {
			systems = append(systems, m.Raw)
			continue
		}
		others = append(others, m.Raw)
	}

	if len(others) == 0 {
		return 0
	}

	// Walk the non-system entries backwards for the latest user message.
	var latestUserRaw string
	for i := len(all) - 1; i >= 0; i-- {
		if all[i].Get("role").String() == "user" {
			latestUserRaw = all[i].Raw
			break
		}
	}

	summaryMsg := openAIAssistantSummaryMessage(summary)
	rebuilt := make([]string, 0, len(systems)+2)
	rebuilt = append(rebuilt, systems...)
	rebuilt = append(rebuilt, summaryMsg)
	preserved := 0
	if latestUserRaw != "" {
		rebuilt = append(rebuilt, latestUserRaw)
		preserved = 1
	}

	// elided counts original conversation messages no longer present;
	// preserved systems and the (optional) latestUser are not counted.
	elided := max(len(others)-preserved, 0)

	newMessages := "[" + strings.Join(rebuilt, ",") + "]"
	out, err := sjson.SetRawBytes(e.body, "messages", []byte(newMessages))
	if err != nil {
		return 0
	}
	e.body = out
	return elided
}

// openAIAssistantSummaryMessage builds an OpenAI assistant message with the
// tagged summary as a single string content.
func openAIAssistantSummaryMessage(summary string) string {
	tagged := HandoverSummaryTag + summary
	msg := map[string]any{
		"role":    "assistant",
		"content": tagged,
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		escaped, _ := json.Marshal(tagged)
		return `{"role":"assistant","content":` + string(escaped) + `}`
	}
	return string(raw)
}

func (e *RequestEnvelope) trimOpenAILastN(n int) int {
	msgs := gjson.GetBytes(e.body, "messages")
	if !msgs.IsArray() {
		return 0
	}
	all := msgs.Array()
	if len(all) == 0 {
		return 0
	}
	systems := make([]string, 0)
	others := make([]string, 0, len(all))
	for _, m := range all {
		if m.Get("role").String() == "system" {
			systems = append(systems, m.Raw)
			continue
		}
		others = append(others, m.Raw)
	}
	if len(others) <= n {
		return 0
	}
	keep := others[len(others)-n:]
	cleaned := stripOrphanedOpenAIToolMessages(keep)
	rebuilt := make([]string, 0, len(systems)+len(cleaned))
	rebuilt = append(rebuilt, systems...)
	rebuilt = append(rebuilt, cleaned...)
	newMessages := "[" + strings.Join(rebuilt, ",") + "]"
	out, err := sjson.SetRawBytes(e.body, "messages", []byte(newMessages))
	if err != nil {
		return 0
	}
	e.body = out
	return len(others) - n
}

// rewriteGeminiForHandover mirrors the Anthropic path against Gemini's
// `contents` array. systemInstruction is untouched.
func (e *RequestEnvelope) rewriteGeminiForHandover(summary string) int {
	contents := gjson.GetBytes(e.body, "contents")
	if !contents.IsArray() {
		return 0
	}
	all := contents.Array()
	if len(all) == 0 {
		return 0
	}

	var latestUser gjson.Result
	for i := len(all) - 1; i >= 0; i-- {
		if all[i].Get("role").String() == "user" {
			latestUser = all[i]
			break
		}
	}

	tagged := HandoverSummaryTag + summary
	summaryEntry := map[string]any{
		"role":  "model",
		"parts": []any{map[string]any{"text": tagged}},
	}
	summaryRaw, _ := json.Marshal(summaryEntry)

	rebuilt := make([]string, 0, 2)
	rebuilt = append(rebuilt, string(summaryRaw))
	preserved := 0
	if latestUser.Exists() {
		rebuilt = append(rebuilt, latestUser.Raw)
		preserved = 1
	}

	elided := max(len(all)-preserved, 0)

	newContents := "[" + strings.Join(rebuilt, ",") + "]"
	out, err := sjson.SetRawBytes(e.body, "contents", []byte(newContents))
	if err != nil {
		return 0
	}
	e.body = out
	return elided
}

func (e *RequestEnvelope) trimGeminiLastN(n int) int {
	contents := gjson.GetBytes(e.body, "contents")
	if !contents.IsArray() {
		return 0
	}
	all := contents.Array()
	if len(all) <= n {
		return 0
	}
	rebuilt := stripLeadingGeminiOrphanFunctionResponses(all[len(all)-n:])
	newContents := "[" + strings.Join(rebuilt, ",") + "]"
	out, err := sjson.SetRawBytes(e.body, "contents", []byte(newContents))
	if err != nil {
		return 0
	}
	e.body = out
	return len(all) - len(rebuilt)
}

// stripOrphanedAnthropicToolResults drops tool_result blocks whose tool_use_id
// has no matching tool_use among the set's assistant messages; user messages
// left empty afterward are omitted entirely. Also returns the number of blocks
// removed, which callers cannot infer from the message count: a user message
// carrying other content survives the strip with the count unchanged.
func stripOrphanedAnthropicToolResults(msgs []gjson.Result) ([]string, int) {
	knownIDs := collectAnthropicToolUseIDs(msgs)
	result := make([]string, 0, len(msgs))
	stripped := 0
	for _, m := range msgs {
		if m.Get("role").String() != "user" {
			result = append(result, m.Raw)
			continue
		}
		cleaned, n := stripAnthropicToolResultMsg(m, knownIDs)
		stripped += n
		if cleaned != "" {
			result = append(result, cleaned)
		}
	}
	return result, stripped
}

// collectAnthropicToolUseIDs returns the set of tool_use IDs present in
// assistant messages.
func collectAnthropicToolUseIDs(msgs []gjson.Result) map[string]struct{} {
	ids := make(map[string]struct{})
	for _, m := range msgs {
		if m.Get("role").String() != "assistant" {
			continue
		}
		m.Get("content").ForEach(func(_, block gjson.Result) bool {
			if block.Get("type").String() == "tool_use" {
				if id := block.Get("id").String(); id != "" {
					ids[id] = struct{}{}
				}
			}
			return true
		})
	}
	return ids
}

// stripAnthropicToolResultMsg removes tool_result blocks not in knownIDs (nil
// strips all). Returns "" if the message is left with no content, plus the
// number of blocks removed.
func stripAnthropicToolResultMsg(msg gjson.Result, knownIDs map[string]struct{}) (string, int) {
	content := msg.Get("content")
	if !content.IsArray() {
		return msg.Raw, 0
	}

	hasOrphans := false
	content.ForEach(func(_, block gjson.Result) bool {
		if block.Get("type").String() == "tool_result" {
			id := block.Get("tool_use_id").String()
			if _, ok := knownIDs[id]; !ok {
				hasOrphans = true
				return false
			}
		}
		return true
	})
	if !hasOrphans {
		return msg.Raw, 0
	}

	var kept []string
	stripped := 0
	content.ForEach(func(_, block gjson.Result) bool {
		if block.Get("type").String() == "tool_result" {
			id := block.Get("tool_use_id").String()
			if _, ok := knownIDs[id]; !ok {
				stripped++
				return true
			}
		}
		kept = append(kept, block.Raw)
		return true
	})
	if len(kept) == 0 {
		return "", stripped
	}
	newContent := "[" + strings.Join(kept, ",") + "]"
	out, err := sjson.SetRawBytes([]byte(msg.Raw), "content", []byte(newContent))
	if err != nil {
		return msg.Raw, 0
	}
	return string(out), stripped
}

// stripOrphanedOpenAIToolMessages removes role:"tool" messages whose
// tool_call_id doesn't match any assistant tool_calls[].id in the set.
func stripOrphanedOpenAIToolMessages(msgs []string) []string {
	knownIDs := make(map[string]struct{})
	for _, raw := range msgs {
		parsed := gjson.Parse(raw)
		if parsed.Get("role").String() != "assistant" {
			continue
		}
		parsed.Get("tool_calls").ForEach(func(_, tc gjson.Result) bool {
			if id := tc.Get("id").String(); id != "" {
				knownIDs[id] = struct{}{}
			}
			return true
		})
	}
	result := make([]string, 0, len(msgs))
	for _, raw := range msgs {
		parsed := gjson.Parse(raw)
		if parsed.Get("role").String() == "tool" {
			tcID := parsed.Get("tool_call_id").String()
			if _, ok := knownIDs[tcID]; !ok {
				continue
			}
		}
		result = append(result, raw)
	}
	return result
}

// stripOrphanedOpenAIToolCalls removes tool_calls entries from assistant
// messages whose id has no matching role:"tool" tool_call_id in the set.
// Returns the modified message list and the count of orphaned tool_calls
// stripped. An assistant message whose tool_calls are all orphaned gets the
// key removed entirely so the field is absent from the wire payload. A
// healthy client-submitted history never has an unanswered tool_calls entry
// (the client's own tool loop always finishes answering one turn's calls
// before sending the next request), so stripping is unconditional — there is
// no legitimate "still in flight" case to protect against.
func stripOrphanedOpenAIToolCalls(msgs []string) ([]string, int) {
	knownIDs := make(map[string]struct{})
	for _, raw := range msgs {
		parsed := gjson.Parse(raw)
		if parsed.Get("role").String() != "tool" {
			continue
		}
		if id := parsed.Get("tool_call_id").String(); id != "" {
			knownIDs[id] = struct{}{}
		}
	}

	result := make([]string, 0, len(msgs))
	stripped := 0
	for _, raw := range msgs {
		parsed := gjson.Parse(raw)
		if parsed.Get("role").String() != "assistant" {
			result = append(result, raw)
			continue
		}
		tc := parsed.Get("tool_calls")
		if !tc.IsArray() || tc.Get("#").Int() == 0 {
			result = append(result, raw)
			continue
		}

		var kept []string
		tc.ForEach(func(_, entry gjson.Result) bool {
			id := entry.Get("id").String()
			if _, ok := knownIDs[id]; ok {
				kept = append(kept, entry.Raw)
			} else {
				stripped++
			}
			return true
		})
		if len(kept) == 0 {
			out, err := sjson.DeleteBytes([]byte(raw), "tool_calls")
			if err != nil {
				result = append(result, raw)
				continue
			}
			result = append(result, string(out))
			continue
		}
		if len(kept) == int(tc.Get("#").Int()) {
			result = append(result, raw)
			continue
		}
		newTC := "[" + strings.Join(kept, ",") + "]"
		out, err := sjson.SetRawBytes([]byte(raw), "tool_calls", []byte(newTC))
		if err != nil {
			result = append(result, raw)
			continue
		}
		result = append(result, string(out))
	}
	return result, stripped
}

// SanitizeOrphanedToolCalls strips tool_calls entries from assistant messages
// that have no matching tool response in the request history, then strips
// tool_result content blocks from user messages whose IDs are unmatched.
// Some providers (Together) enforce strict tool-call/response pairing and 400
// on orphaned IDs that other providers silently accept. This pass runs once
// per turn so a session is never bricked by a single mid-stream failure that
// left a dangling tool_use block on the wire. Returns the total count of
// blocks/messages stripped; zero when clean or when the format is Gemini
// (Gemini doesn't validate pairing).
func (e *RequestEnvelope) SanitizeOrphanedToolCalls() int {
	if e == nil {
		return 0
	}
	switch e.format {
	case FormatOpenAI:
		return e.sanitizeOrphanedOpenAIToolCalls()
	case FormatAnthropic:
		return e.sanitizeOrphanedAnthropicToolCalls()
	default:
		return 0
	}
}

func (e *RequestEnvelope) sanitizeOrphanedOpenAIToolCalls() int {
	msgs := gjson.GetBytes(e.body, "messages")
	if !msgs.IsArray() {
		return 0
	}
	all := msgs.Array()
	if len(all) == 0 {
		return 0
	}
	raws := make([]string, 0, len(all))
	for _, m := range all {
		raws = append(raws, m.Raw)
	}
	cleanedCalls, callStripped := stripOrphanedOpenAIToolCalls(raws)
	cleaned := stripOrphanedOpenAIToolMessages(cleanedCalls)
	resultStripped := len(raws) - len(cleaned)
	if callStripped == 0 && resultStripped == 0 {
		return 0
	}
	newMessages := "[" + strings.Join(cleaned, ",") + "]"
	out, err := sjson.SetRawBytes(e.body, "messages", []byte(newMessages))
	if err != nil {
		return 0
	}
	e.body = out
	return callStripped + resultStripped
}

// collectAnthropicToolResultIDs returns the set of tool_use_ids referenced by
// tool_result blocks in user messages. Reverse of collectAnthropicToolUseIDs.
func collectAnthropicToolResultIDs(msgs []gjson.Result) map[string]struct{} {
	ids := make(map[string]struct{})
	for _, m := range msgs {
		if m.Get("role").String() != "user" {
			continue
		}
		m.Get("content").ForEach(func(_, block gjson.Result) bool {
			if block.Get("type").String() == "tool_result" {
				if id := block.Get("tool_use_id").String(); id != "" {
					ids[id] = struct{}{}
				}
			}
			return true
		})
	}
	return ids
}

// stripOrphanedAnthropicToolCalls removes tool_use content blocks from
// assistant messages whose id has no matching tool_result in the set.
// Returns the number of orphaned tool_use blocks stripped. Unconditional
// like stripOrphanedOpenAIToolCalls: a well-formed client history never ends
// on (or contains) an unanswered tool_use, so there's no legitimate
// in-flight case a knownIDs-empty guard would need to protect.
func stripOrphanedAnthropicToolCalls(msgs []gjson.Result) (int, []gjson.Result) {
	knownIDs := collectAnthropicToolResultIDs(msgs)

	result := make([]gjson.Result, 0, len(msgs))
	stripped := 0
	for _, m := range msgs {
		if m.Get("role").String() != "assistant" {
			result = append(result, m)
			continue
		}
		content := m.Get("content")
		if !content.IsArray() {
			result = append(result, m)
			continue
		}

		var kept []string
		content.ForEach(func(_, block gjson.Result) bool {
			if block.Get("type").String() == "tool_use" {
				id := block.Get("id").String()
				if _, ok := knownIDs[id]; !ok {
					stripped++
					return true
				}
			}
			kept = append(kept, block.Raw)
			return true
		})
		if len(kept) == len(content.Array()) {
			result = append(result, m)
			continue
		}
		if len(kept) == 0 {
			// Drop the message entirely — no content left after
			// stripping orphaned tool_use blocks.
			continue
		}
		newContent := "[" + strings.Join(kept, ",") + "]"
		out, err := sjson.SetRawBytes([]byte(m.Raw), "content", []byte(newContent))
		if err != nil {
			result = append(result, m)
			continue
		}
		result = append(result, gjson.ParseBytes(out))
	}
	return stripped, result
}

func (e *RequestEnvelope) sanitizeOrphanedAnthropicToolCalls() int {
	msgs := gjson.GetBytes(e.body, "messages")
	if !msgs.IsArray() {
		return 0
	}
	all := msgs.Array()
	if len(all) == 0 {
		return 0
	}
	callStripped, cleaned := stripOrphanedAnthropicToolCalls(all)
	// Both passes run unconditionally. An orphaned tool_result can be present
	// with no orphaned tool_use at all (a resumed session whose assistant turn
	// was never persisted), and Anthropic rejects that wherever it appears —
	// unlike an unanswered tool_use, which only 400s as the final message.
	cleanedRaw, resultStripped := stripOrphanedAnthropicToolResults(cleaned)
	if callStripped == 0 && resultStripped == 0 {
		return 0
	}
	totalStripped := callStripped + resultStripped
	newMessages := "[" + strings.Join(cleanedRaw, ",") + "]"
	out, err := sjson.SetRawBytes(e.body, "messages", []byte(newMessages))
	if err != nil {
		return 0
	}
	e.body = out
	return totalStripped
}
