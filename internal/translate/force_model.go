package translate

import (
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const ReasonUserForceModel = "user_forced"

const ReasonLoopEscalation = "loop_escalation"

type ForceModelResult struct {
	Model string

	ParentOnly bool

	Clear bool

	FromToolResult bool
}

func (e *RequestEnvelope) ExtractForceModelCommand() (ForceModelResult, bool) {
	var res ForceModelResult
	found, fromToolResult := e.extractLeadingCommandWithSource(func(text string, fromToolResult bool) (bool, string) {
		r, ok, stripped := parseForceModelCommandFromSource(text, fromToolResult)
		if ok {
			res = r
		}
		return ok, stripped
	})
	res.FromToolResult = found && fromToolResult
	return res, found
}

func (e *RequestEnvelope) extractLeadingCommand(parse func(text string) (found bool, stripped string)) bool {
	originalBody := e.body
	found, fromToolResult := e.extractLeadingCommandWithSource(func(text string, _ bool) (bool, string) {
		return parse(text)
	})
	if fromToolResult {
		e.body = originalBody
		return false
	}
	return found
}

func isSkillAttachment(msgs []gjson.Result, i int) bool {
	if i <= 0 || msgs[i].Get("role").String() != "user" {
		return false
	}
	if msgs[i-1].Get("role").String() != "user" {
		return false
	}
	return isSkillInstructionsOnly(msgs[i].Get("content"))
}

func isSkillInstructionsOnly(content gjson.Result) bool {
	var text string
	switch {
	case content.Type == gjson.String:
		text = content.String()
	case content.IsArray():
		var b strings.Builder
		for _, part := range content.Array() {
			switch part.Get("type").String() {
			case "text", "input_text":
				b.WriteString(part.Get("text").String())
			default:

				return false
			}
		}
		text = b.String()
	default:
		return false
	}
	text = strings.TrimSpace(text)
	return strings.HasPrefix(text, "<skill>") && strings.HasSuffix(text, "</skill>")
}

type commandTextCandidate struct {
	path           string
	dropPath       string
	text           string
	fromToolResult bool
}

func (e *RequestEnvelope) extractLeadingCommandWithSource(parse func(text string, fromToolResult bool) (found bool, stripped string)) (bool, bool) {
	switch e.format {
	case FormatAnthropic, FormatOpenAI:
	default:
		return false, false
	}
	msgs := viewJSONPath(e.body, "messages")
	if !msgs.IsArray() {
		return false, false
	}

	all := msgs.Array()
	lastIdx := -1
	lastRole := ""
	var lastContent gjson.Result
	for i := len(all) - 1; i >= 0; i-- {
		role := all[i].Get("role").String()

		if isSkillAttachment(all, i) {
			continue
		}
		switch role {
		case "user", "tool":
			lastIdx, lastRole, lastContent = i, role, all[i].Get("content")
		}
		if lastIdx >= 0 {
			break
		}
	}
	if lastIdx < 0 {
		return false, false
	}

	for i := lastIdx + 1; i < len(all); i++ {
		if isSkillAttachment(all, i) {
			continue
		}
		if isConversationTurn(all[i].Get("role").String()) {
			return false, false
		}
	}

	idxPath := "messages." + strconv.Itoa(lastIdx) + ".content"

	isToolMessage := lastRole == "tool"
	fromToolResult := isToolMessage || followsAssistantToolUse(all, lastIdx)
	dropPathFor := func(path string) string {
		if isToolMessage {
			return ""
		}
		return path
	}
	var candidates []commandTextCandidate
	switch {
	case lastContent.Type == gjson.String:
		candidates = append(candidates, commandTextCandidate{
			path: idxPath, dropPath: dropPathFor("messages." + strconv.Itoa(lastIdx)), text: lastContent.String(), fromToolResult: isToolMessage,
		})
	case lastContent.IsArray():
		lastContent.ForEach(func(key, block gjson.Result) bool {
			blockPath := idxPath + "." + strconv.Itoa(int(key.Int()))
			switch block.Get("type").String() {
			case "text":
				candidates = append(candidates, commandTextCandidate{
					path: blockPath + ".text", dropPath: dropPathFor(blockPath), text: block.Get("text").String(), fromToolResult: isToolMessage,
				})
			case "tool_result":
				fromToolResult = true
				candidates = append(candidates, toolResultCommandCandidates(blockPath, block.Get("content"))...)
			}
			return true
		})
	default:
		return false, false
	}

	for _, candidate := range candidates {
		found, stripped := parse(strings.Clone(candidate.text), candidate.fromToolResult)
		if !found && !candidate.fromToolResult {
			if before, query, after, ok := typedMessageInWrapper(candidate.text); ok {
				if found, stripped = parse(strings.Clone(query), candidate.fromToolResult); found {
					stripped = before + stripped + after
				}
			}
		}
		if !found {
			continue
		}
		if fromToolResult && stripped == "" && candidate.dropPath != "" {
			if newBody, ok := dropCommandBlock(e.body, candidate.dropPath, lastIdx); ok {
				e.body = newBody
				return true, fromToolResult
			}
		}
		if newBody, err := sjson.SetBytes(e.body, candidate.path, stripped); err == nil {
			e.body = newBody
		}
		return true, fromToolResult
	}
	return false, false
}

const (
	cursorUserQueryOpen  = "<user_query>"
	cursorUserQueryClose = "</user_query>"
)

func cursorUserQuery(text string) (before, query, after string, ok bool) {
	open := strings.Index(text, cursorUserQueryOpen)
	if open < 0 {
		return "", "", "", false
	}
	head := text[:open]
	if strings.TrimSpace(head[leadingInjectedPrefixEnd(head):]) != "" {
		return "", "", "", false
	}
	closeIdx := strings.LastIndex(text, cursorUserQueryClose)
	if closeIdx < open || strings.TrimSpace(text[closeIdx+len(cursorUserQueryClose):]) != "" {
		return "", "", "", false
	}
	start := open + len(cursorUserQueryOpen)
	return text[:start], text[start:closeIdx], text[closeIdx:], true
}

func parseInCursorQuery[T any](text string, parse func(string) (T, bool, string)) (T, bool, string) {
	res, found, stripped := parse(text)
	if found {
		return res, found, stripped
	}
	if before, query, after, ok := typedMessageInWrapper(text); ok {
		if inner, innerFound, innerStripped := parse(query); innerFound {
			if strings.TrimSpace(innerStripped) == "" {
				return inner, true, strings.TrimSuffix(before, cursorUserQueryOpen)
			}
			return inner, true, before + innerStripped + after
		}
	}
	return res, found, stripped
}

func dropCommandBlock(body []byte, dropPath string, msgIdx int) ([]byte, bool) {
	out, err := sjson.DeleteBytes(body, dropPath)
	if err != nil {
		return nil, false
	}
	msgPath := "messages." + strconv.Itoa(msgIdx)
	if dropPath != msgPath && gjson.GetBytes(out, msgPath+".content").Get("#").Int() == 0 {
		if out, err = sjson.DeleteBytes(out, msgPath); err != nil {
			return nil, false
		}
	}
	if gjson.GetBytes(out, "messages").Get("#").Int() == 0 {
		return nil, false
	}
	return out, true
}

func toolResultCommandCandidates(blockPath string, content gjson.Result) []commandTextCandidate {
	if content.Type == gjson.String {
		return []commandTextCandidate{{
			path:           blockPath + ".content",
			text:           content.String(),
			fromToolResult: true,
		}}
	}
	if !content.IsArray() {
		return nil
	}
	var candidates []commandTextCandidate
	content.ForEach(func(key, part gjson.Result) bool {
		if part.Get("type").String() == "text" {
			candidates = append(candidates, commandTextCandidate{
				path:           blockPath + ".content." + strconv.Itoa(int(key.Int())) + ".text",
				text:           part.Get("text").String(),
				fromToolResult: true,
			})
		}
		return true
	})
	return candidates
}

func isConversationTurn(role string) bool {
	switch role {
	case "user", "assistant", "tool":
		return true
	}
	return false
}

func followsAssistantToolUse(messages []gjson.Result, userIdx int) bool {
	prev := -1
	for i := userIdx - 1; i >= 0; i-- {
		if isConversationTurn(messages[i].Get("role").String()) {
			prev = i
			break
		}
	}
	if prev < 0 || messages[prev].Get("role").String() != "assistant" {
		return false
	}
	assistant := messages[prev]
	if toolCalls := assistant.Get("tool_calls"); toolCalls.IsArray() && len(toolCalls.Array()) > 0 {
		return true
	}
	content := assistant.Get("content")
	if content.IsArray() {
		for _, block := range content.Array() {
			if block.Get("type").String() == "tool_use" {
				return true
			}
		}
	}
	return false
}

func parseForceModelCommandFromSource(text string, fromToolResult bool) (res ForceModelResult, found bool, stripped string) {
	prefixEnd := leadingInjectedPrefixEnd(text)
	prefix := text[:prefixEnd]
	body := text[prefixEnd:]

	lines := strings.Split(body, "\n")
	cmdIdx := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if after, ok := cutAnyPrefix(trimmed, "/force-model ", "/model ", "/fm ", "$force-model ", "$fm ", "/force-parent ", "$force-parent "); ok {
			if fromToolResult && strings.HasPrefix(trimmed, "/model ") {
				return ForceModelResult{}, false, text
			}

			if name := strings.Join(strings.Fields(after), " "); name != "" {
				res = ForceModelResult{Model: name, ParentOnly: strings.HasPrefix(trimmed, "/force-parent ") || strings.HasPrefix(trimmed, "$force-parent ")}
				found = true
				cmdIdx = i
			}
		} else if trimmed == "/unforce-model" || trimmed == "/ufm" || trimmed == "$unforce-model" || trimmed == "$ufm" {
			res = ForceModelResult{Clear: true}
			found = true
			cmdIdx = i
		}
		break
	}
	if !found {

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
					candidate, ok, _ := parseForceModelCommandFromSource(lines[i], fromToolResult)
					if !ok {
						break
					}
					remaining := append([]string{}, lines[:i]...)
					remaining = append(remaining, lines[i+1:]...)
					return candidate, true, strings.TrimSpace(strings.Join(remaining, "\n"))
				}
				break
			}
		}
		return ForceModelResult{}, false, text
	}
	remaining := make([]string, 0, len(lines))
	remaining = append(remaining, lines[:cmdIdx]...)
	remaining = append(remaining, lines[cmdIdx+1:]...)
	bodyStripped := strings.Join(remaining, "\n")
	stripped = strings.TrimSpace(prefix + bodyStripped)
	return res, true, stripped
}

func cutAnyPrefix(text string, prefixes ...string) (after string, ok bool) {
	for _, p := range prefixes {
		if after, ok = strings.CutPrefix(text, p); ok {
			return after, true
		}
	}
	return text, false
}

func leadingInjectedPrefixEnd(text string) int {
	i := 0
	for i < len(text) {

		j := i
		for j < len(text) {
			c := text[j]
			if c != ' ' && c != '\t' && c != '\r' && c != '\n' {
				break
			}
			j++
		}
		if j >= len(text) || text[j] != '<' {
			return i
		}

		nameStart := j + 1
		nameEnd := nameStart
		for nameEnd < len(text) {
			c := text[nameEnd]
			if c == '>' {
				break
			}
			if !isTagNameByte(c, nameEnd == nameStart) {
				return i
			}
			nameEnd++
		}
		if nameEnd >= len(text) || nameEnd == nameStart {
			return i
		}
		closeTag := "</" + text[nameStart:nameEnd] + ">"
		closeIdx := strings.Index(text[nameEnd+1:], closeTag)
		if closeIdx < 0 {
			return i
		}
		i = nameEnd + 1 + closeIdx + len(closeTag)
	}
	return i
}

func isOnlyKnownInjectedText(text string) bool {
	rest := strings.TrimSpace(text)
	for rest != "" {

		blockEnd := leadingClaudeCodeInjectedBlockEnd(rest)
		if blockEnd == 0 {
			return false
		}
		rest = strings.TrimSpace(rest[blockEnd:])
	}
	return true
}

func isTagNameByte(c byte, first bool) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		return true
	case !first && (c >= '0' && c <= '9' || c == '-' || c == '_'):
		return true
	default:
		return false
	}
}
