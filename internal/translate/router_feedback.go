package translate

import (
	"strings"

	"github.com/tidwall/gjson"
)

type RouterFeedbackResult struct {
	Rating         string
	SuggestedLabel string
	Feedback       string
	Sequence       int
	FromToolResult bool
}

const (
	RouterFeedbackRatingUp   = "up"
	RouterFeedbackRatingDown = "down"
)

var RouterFeedbackLabels = map[string]struct{}{
	"fast":     {},
	"explore":  {},
	"balanced": {},
	"high":     {},
	"maximum":  {},
}

func (e *RequestEnvelope) ExtractRouterFeedbackCommand() (RouterFeedbackResult, bool) {
	var res RouterFeedbackResult
	found, fromToolResult := e.extractLeadingCommandWithSource(func(text string, _ bool) (bool, string) {
		r, ok, stripped := parseRouterFeedbackCommand(text)
		if ok {
			res = r
		}
		return ok, stripped
	})
	res.FromToolResult = found && fromToolResult
	return res, found
}

func (e *RequestEnvelope) StripRouterFeedbackArtifacts() int {
	switch e.format {
	case FormatAnthropic, FormatOpenAI:
	default:
		return 0
	}
	msgs := viewJSONPath(e.body, "messages")
	if !msgs.IsArray() {
		return 0
	}

	all := msgs.Array()
	lastUserIdx := -1
	for i := range all {
		if all[i].Get("role").String() != "user" {
			continue
		}
		if isSkillAttachment(all, i) {
			continue
		}
		lastUserIdx = i
	}

	removed := 0
	rebuilt := make([]string, 0, len(msgs.Array()))
	msgs.ForEach(func(key, msg gjson.Result) bool {
		idx := int(key.Int())
		role := msg.Get("role").String()
		content := msg.Get("content")
		if role == "user" && idx != lastUserIdx && isRouterFeedbackCommandOnlyContent(content) {
			removed++
			return true
		}
		if role == "assistant" && isRouterFeedbackAckOnlyContent(content) {
			removed++
			return true
		}
		rebuilt = append(rebuilt, msg.Raw)
		return true
	})
	if removed == 0 {
		return 0
	}
	return e.setMessages(rebuilt, removed)
}

func parseRouterFeedbackCommand(text string) (res RouterFeedbackResult, found bool, stripped string) {
	prefixEnd := leadingInjectedPrefixEnd(text)
	prefix := text[:prefixEnd]
	body := text[prefixEnd:]

	trimmedBody := strings.TrimSpace(body)
	first, rest, _ := strings.Cut(trimmedBody, "\n")
	first = strings.TrimSpace(first)

	rating, inline, ok := matchRouterFeedbackCommand(first)
	if !ok {
		if strings.HasPrefix(strings.TrimSpace(text), "Script completed") {
			lines := strings.Split(text, "\n")
			for i, line := range lines {
				if strings.TrimSpace(line) != "Output:" {
					continue
				}
				for i++; i < len(lines); i++ {
					if strings.TrimSpace(lines[i]) == "" {
						continue
					}
					candidate, found, _ := parseRouterFeedbackCommand(strings.Join(lines[i:], "\n"))
					if !found {
						break
					}
					candidate.FromToolResult = false
					return candidate, true, strings.TrimSpace(strings.Join(lines[:i], "\n"))
				}
				break
			}
		}
		return RouterFeedbackResult{}, false, text
	}

	feedback := strings.TrimSpace(inline)
	if rest = strings.TrimSpace(rest); rest != "" {
		if feedback != "" {
			feedback += "\n"
		}
		feedback += rest
	}
	var seq int
	seq, feedback = splitSequence(feedback)
	if rating == "" {
		rating, feedback = splitLeadingRating(feedback)
	}
	var label string
	if rating == RouterFeedbackRatingDown {
		label, feedback = stripTrailingLabel(feedback)
	}
	return RouterFeedbackResult{Rating: rating, SuggestedLabel: label, Feedback: feedback, Sequence: seq}, true, strings.TrimSpace(prefix)
}

func isRouterFeedbackCommandOnlyContent(content gjson.Result) bool {
	switch {
	case content.Type == gjson.String:
		_, found, stripped := parseInCursorQuery(content.String(), parseRouterFeedbackCommand)
		return found && isOnlyInjectedCommandText(stripped)
	case content.Type == gjson.JSON && content.IsArray():
		seenCommand := false
		allSynthetic := true
		content.ForEach(func(_, block gjson.Result) bool {
			if block.Get("type").String() != "text" {
				allSynthetic = false
				return false
			}
			text := block.Get("text").String()
			if _, found, stripped := parseInCursorQuery(text, parseRouterFeedbackCommand); found && isOnlyInjectedCommandText(stripped) {
				seenCommand = true
				return true
			}
			if strings.TrimSpace(text) == "" || isClaudeCodeInjectedBlock(text) || isOnlyInjectedCommandText(text) {
				return true
			}
			allSynthetic = false
			return false
		})
		return seenCommand && allSynthetic
	default:
		return false
	}
}

func isOnlyInjectedCommandText(text string) bool {
	trimmed := strings.TrimSpace(text)
	return trimmed == "" || leadingInjectedPrefixEnd(trimmed) == len(trimmed)
}

func isRouterFeedbackAckOnlyContent(content gjson.Result) bool {
	switch {
	case content.Type == gjson.String:
		return isRouterFeedbackAckText(content.String())
	case content.Type == gjson.JSON && content.IsArray():
		seenAck := false
		allSynthetic := true
		content.ForEach(func(_, block gjson.Result) bool {
			if block.Get("type").String() != "text" {
				allSynthetic = false
				return false
			}
			text := block.Get("text").String()
			if strings.TrimSpace(text) == "" {
				return true
			}
			if isRouterFeedbackAckText(text) {
				seenAck = true
				return true
			}
			allSynthetic = false
			return false
		})
		return seenAck && allSynthetic
	default:
		return false
	}
}

func isRouterFeedbackAckText(text string) bool {
	trimmed := strings.TrimSpace(text)
	return strings.HasPrefix(trimmed, "Weave Router: Feedback recorded") ||
		strings.HasPrefix(trimmed, "Weave Router: router-feedback needs a verdict") ||
		strings.HasPrefix(trimmed, "Weave Router: No turn found at that sequence number") ||
		strings.HasPrefix(trimmed, "✦ **Weave Router** → Feedback recorded") ||
		strings.HasPrefix(trimmed, "✦ **Weave Router** → Router-feedback needs a verdict") ||
		strings.HasPrefix(trimmed, "✦ **Weave Router** → No turn found at that sequence number")
}

func matchRouterFeedbackCommand(first string) (rating, inline string, ok bool) {
	for _, c := range []struct{ tok, rating string }{
		{"/rf+", RouterFeedbackRatingUp},
		{"$rf+", RouterFeedbackRatingUp},
		{"/rf👍", RouterFeedbackRatingUp},
		{"$rf👍", RouterFeedbackRatingUp},
		{"/rf-", RouterFeedbackRatingDown},
		{"$rf-", RouterFeedbackRatingDown},
		{"/rf👎", RouterFeedbackRatingDown},
		{"$rf👎", RouterFeedbackRatingDown},
	} {
		if first == c.tok {
			return c.rating, "", true
		}
		if after, found := strings.CutPrefix(first, c.tok+" "); found {
			return c.rating, after, true
		}
	}
	if first == "/router-feedback" || first == "/rf" || first == "$router-feedback" || first == "$rf" {
		return "", "", true
	}
	if after, found := cutAnyPrefix(first, "/router-feedback ", "/rf ", "$router-feedback ", "$rf "); found {
		return "", after, true
	}
	return "", "", false
}

func splitLeadingRating(s string) (rating, rest string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", ""
	}
	tok, after, _ := strings.Cut(s, " ")
	switch strings.ToLower(tok) {
	case "+", "👍", "up", "thumbsup":
		return RouterFeedbackRatingUp, strings.TrimSpace(after)
	case "-", "👎", "down", "thumbsdown":
		return RouterFeedbackRatingDown, strings.TrimSpace(after)
	}
	return "", s
}

func stripTrailingLabel(s string) (label, cleaned string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", ""
	}

	idx := strings.LastIndex(s, "--label=")
	if idx < 0 {
		return "", s
	}

	rest := s[idx+len("--label="):]
	val, tail, ok := extractQuotedOrBareValue(rest)
	if !ok || strings.TrimSpace(tail) != "" {
		return "", s
	}
	val = strings.ToLower(strings.TrimSpace(val))
	if _, valid := RouterFeedbackLabels[val]; !valid {
		return "", s
	}

	cleaned = strings.TrimSpace(s[:idx])
	return val, cleaned
}

func extractQuotedOrBareValue(s string) (val, rest string, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", "", false
	}
	if s[0] == '"' {
		end := strings.IndexByte(s[1:], '"')
		if end < 0 {
			return "", "", false
		}
		return s[1 : end+1], s[end+2:], true
	}
	val, tail, _ := strings.Cut(s, " ")
	return val, tail, true
}

func splitSequence(s string) (seq int, rest string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, ""
	}
	tok, after, _ := strings.Cut(s, " ")
	if tok == "-" || tok == "-0" {
		return 0, s
	}
	if strings.HasPrefix(tok, "-") && len(tok) > 1 && tok[1] >= '1' && tok[1] <= '9' {
		if len(tok) > 3 {
			return 0, s
		}
		restDigits := tok[2:]
		allDigits := true
		for i := 0; i < len(restDigits); i++ {
			if restDigits[i] < '0' || restDigits[i] > '9' {
				allDigits = false
				break
			}
		}
		if allDigits {
			seq := 0
			for _, ch := range tok[1:] {
				seq = seq*10 + int(ch-'0')
			}
			return -seq, strings.TrimSpace(after)
		}
		return 0, s
	}
	allDigits := len(tok) > 0
	for i := 0; i < len(tok); i++ {
		if tok[i] < '0' || tok[i] > '9' {
			allDigits = false
			break
		}
	}
	if allDigits {
		if len(tok) > 2 {
			return 0, s
		}
		seq := 0
		for _, ch := range tok {
			seq = seq*10 + int(ch-'0')
		}
		if seq == 0 {
			return 0, s
		}
		return seq, strings.TrimSpace(after)
	}
	return 0, s
}
