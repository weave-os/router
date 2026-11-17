package translate

import (
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const HandoverSummaryTag = "[handover summary] "

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

func (e *RequestEnvelope) rewriteAnthropicForHandover(summary string) int {
	msgs := gjson.GetBytes(e.body, "messages")
	if !msgs.IsArray() {
		return 0
	}
	all := msgs.Array()
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

	summaryBlock := anthropicAssistantSummaryBlock(summary)
	rebuilt := []string{summaryBlock}
	preserved := 0
	if latestUser.Exists() {

		cleaned, _ := stripAnthropicToolResultMsg(latestUser, nil)
		if cleaned != "" {
			rebuilt = append(rebuilt, cleaned)
			preserved = 1
		}
	}

	elided := max(len(all)-preserved, 0)

	newMessages := "[" + strings.Join(rebuilt, ",") + "]"
	out, err := sjson.SetRawBytes(e.body, "messages", []byte(newMessages))
	if err != nil {
		return 0
	}
	e.body = out
	return elided
}

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
	keep, _ := recentMessagesWithUserTextBoundary(all, n, FormatAnthropic)
	rebuilt, _ := stripOrphanedAnthropicToolResults(keep)
	newMessages := "[" + strings.Join(rebuilt, ",") + "]"
	out, err := sjson.SetRawBytes(e.body, "messages", []byte(newMessages))
	if err != nil {
		return 0
	}
	e.body = out
	return len(all) - n
}

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

	elided := max(len(others)-preserved, 0)

	newMessages := "[" + strings.Join(rebuilt, ",") + "]"
	out, err := sjson.SetRawBytes(e.body, "messages", []byte(newMessages))
	if err != nil {
		return 0
	}
	e.body = out
	return elided
}

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
	others := make([]gjson.Result, 0, len(all))
	for _, m := range all {
		if m.Get("role").String() == "system" {
			systems = append(systems, m.Raw)
			continue
		}
		others = append(others, m)
	}
	if len(others) <= n {
		return 0
	}
	keep, _ := recentMessagesWithUserTextBoundary(others, n, FormatOpenAI)
	keepRaw := make([]string, 0, len(keep))
	for _, message := range keep {
		keepRaw = append(keepRaw, message.Raw)
	}
	cleaned := stripOrphanedOpenAIToolMessages(keepRaw)
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
	keep, boundaryPulledBack := recentMessagesWithUserTextBoundary(all, n, FormatGemini)
	var rebuilt []string
	if boundaryPulledBack {
		rebuilt = append([]string{keep[0].Raw}, stripLeadingGeminiOrphanFunctionResponses(keep[1:])...)
	} else {
		rebuilt = stripLeadingGeminiOrphanFunctionResponses(keep)
	}
	newContents := "[" + strings.Join(rebuilt, ",") + "]"
	out, err := sjson.SetRawBytes(e.body, "contents", []byte(newContents))
	if err != nil {
		return 0
	}
	e.body = out
	return len(all) - len(rebuilt)
}

func recentMessagesWithUserTextBoundary(messages []gjson.Result, limit int, format Format) ([]gjson.Result, bool) {
	if limit <= 0 || len(messages) <= limit {
		return messages, false
	}
	recent := messages[len(messages)-limit:]
	for i := len(recent) - 1; i >= 0; i-- {
		if isTextBearingUserMessage(recent[i], format) {
			return recent, false
		}
	}
	for i := len(messages) - limit - 1; i >= 0; i-- {
		if !isTextBearingUserMessage(messages[i], format) {
			continue
		}
		preserved := []gjson.Result{messages[i]}
		for suffixStart := 1; suffixStart < len(recent); suffixStart++ {
			if isAssistantMessage(recent[suffixStart], format) {
				return append(preserved, recent[suffixStart:]...), true
			}
		}
		return preserved, true
	}
	return recent, false
}

func isAssistantMessage(message gjson.Result, format Format) bool {
	role := message.Get("role").String()
	if format == FormatGemini {
		return role == "model"
	}
	return role == "assistant"
}

func isTextBearingUserMessage(message gjson.Result, format Format) bool {
	switch format {
	case FormatAnthropic:
		return message.Get("role").String() == "user" &&
			strings.TrimSpace(userPromptTextGJSON(message.Get("content"))) != ""
	case FormatOpenAI:
		return message.Get("role").String() == "user" &&
			strings.TrimSpace(openAIContentTextGJSON(message.Get("content"))) != ""
	case FormatGemini:
		role := message.Get("role").String()
		return (role == "user" || role == "") && strings.TrimSpace(geminiPartsText(message.Get("parts"))) != ""
	default:
		return false
	}
}

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
	msgs := viewJSONPath(e.body, "messages")
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
