package translate

import (
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ReasonUserForceModel marks a session pin from /force-model. It's an
// immutable sticky: scorer/planner are bypassed until /unforce-model clears it.
const ReasonUserForceModel = "user_forced"

// ReasonLoopEscalation marks a session pin created when the router detects a
// tool-call loop and escalates to opus. Immutable sticky like
// ReasonUserForceModel, so the session doesn't re-route back into the loop.
const ReasonLoopEscalation = "loop_escalation"

// ReasonStruggleEscalation marks a session pin created when the struggle
// detector arms an early sideways move (turns >= 30, wall >= 10m). Immutable
// sticky like ReasonLoopEscalation.
const ReasonStruggleEscalation = "struggle_escalation"

// ForceModelResult holds the parsed outcome of a force-model command.
type ForceModelResult struct {
	// Model is the target model name; empty when Clear is true.
	Model string
	// Clear is true for /unforce-model.
	Clear bool
	// FromToolResult is true when an agent invoked the command through a tool.
	FromToolResult bool
}

// ExtractForceModelCommand scans the trailing user or tool-result message in env for a
// /force-model <model> or /unforce-model directive, stripping it from env.body.
// FromToolResult distinguishes agent-issued commands from user-typed ones.
// Returns (zero, false) when no command is present.
func (env *RequestEnvelope) ExtractForceModelCommand() (ForceModelResult, bool) {
	var res ForceModelResult
	found, fromToolResult := env.extractLeadingCommandWithSource(func(text string) (bool, string) {
		r, ok, stripped := parseForceModelCommand(text)
		if ok {
			res = r
		}
		return ok, stripped
	})
	res.FromToolResult = found && fromToolResult
	return res, found
}

// extractLeadingCommand scans the trailing user or tool-result message
// (Anthropic/OpenAI shapes only) for a directive recognized by parse.
func (env *RequestEnvelope) extractLeadingCommand(parse func(text string) (found bool, stripped string)) bool {
	originalBody := env.body
	found, fromToolResult := env.extractLeadingCommandWithSource(parse)
	if fromToolResult {
		env.body = originalBody
		return false
	}
	return found
}

type commandTextCandidate struct {
	path     string
	dropPath string
	text     string
}

// extractLeadingCommandWithSource returns whether the command came from a
// tool-result turn as well as whether it matched.
func (env *RequestEnvelope) extractLeadingCommandWithSource(parse func(text string) (found bool, stripped string)) (bool, bool) {
	switch env.format {
	case FormatAnthropic, FormatOpenAI:
	default:
		return false, false
	}
	msgs := gjson.GetBytes(env.body, "messages")
	if !msgs.IsArray() {
		return false, false
	}

	all := msgs.Array()
	lastIdx := -1
	lastRole := ""
	var lastContent gjson.Result
	for i := len(all) - 1; i >= 0; i-- {
		switch role := all[i].Get("role").String(); role {
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
	// Commands belong only to the trailing turn; skip if a conversational turn
	// follows. Non-conversational role:"system" notices (Claude Code deferred
	// tools) don't count as a newer turn.
	for i := lastIdx + 1; i < len(all); i++ {
		if isConversationTurn(all[i].Get("role").String()) {
			return false, false
		}
	}

	idxPath := "messages." + strconv.Itoa(lastIdx) + ".content"
	// OpenAI marks tool-result provenance with role:"tool", not a content block.
	// Preserve that message when stripping the command so tool_calls stays paired.
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
			path: idxPath, dropPath: dropPathFor("messages." + strconv.Itoa(lastIdx)), text: lastContent.String(),
		})
	case lastContent.IsArray():
		lastContent.ForEach(func(key, block gjson.Result) bool {
			blockPath := idxPath + "." + strconv.Itoa(int(key.Int()))
			switch block.Get("type").String() {
			case "text":
				candidates = append(candidates, commandTextCandidate{
					path: blockPath + ".text", dropPath: dropPathFor(blockPath), text: block.Get("text").String(),
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
		found, stripped := parse(candidate.text)
		if !found {
			continue
		}
		if fromToolResult && stripped == "" && candidate.dropPath != "" {
			if newBody, ok := dropCommandBlock(env.body, candidate.dropPath, lastIdx); ok {
				env.body = newBody
				return true, fromToolResult
			}
		}
		if newBody, err := sjson.SetBytes(env.body, candidate.path, stripped); err == nil {
			env.body = newBody
		}
		return true, fromToolResult
	}
	return false, false
}

// dropCommandBlock deletes the block at dropPath, cascading to the whole
// message when that empties its content array, and reports false when the
// result would be an empty history (providers reject empty content arrays).
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
		return []commandTextCandidate{{path: blockPath + ".content", text: content.String()}}
	}
	if !content.IsArray() {
		return nil
	}
	var candidates []commandTextCandidate
	content.ForEach(func(key, part gjson.Result) bool {
		if part.Get("type").String() == "text" {
			candidates = append(candidates, commandTextCandidate{
				path: blockPath + ".content." + strconv.Itoa(int(key.Int())) + ".text",
				text: part.Get("text").String(),
			})
		}
		return true
	})
	return candidates
}

// isConversationTurn reports whether role is part of the user/assistant
// exchange, as opposed to an out-of-band notice the client interleaves.
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

// parseForceModelCommand scans text for a /force-model (alias /fm) or
// /unforce-model (alias /ufm) directive on the first non-empty line.
// Restricted to the leading line so pasted content (snippets, transcripts)
// starting with "/" can't silently rewrite session routing. The short
// aliases are a fallback for clients without local slash-command expansion
// (pi, opencode, raw API); Claude Code/Codex expand to the canonical form
// client-side.
//
// The whole rest of the command line is the model name: same-line text is
// inseparable from a multi-word model name, so put the prompt on the next
// line. Prevents silently pinning the "qwen" alias when the user typed
// "qwen 3.8" and having the ack look like it took.
//
// Leading <tag>...</tag> blocks (e.g. <system-reminder>, <command-name>
// injected by Claude Code) are skipped before the leading-line check, and
// preserved in the stripped output.
func parseForceModelCommand(text string) (res ForceModelResult, found bool, stripped string) {
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
		if after, ok := cutAnyPrefix(trimmed, "/force-model ", "/fm "); ok {
			// Fields+Join collapses runs of whitespace so "/fm  qwen   3.8"
			// and "/fm qwen 3.8" are the same string to the resolver.
			if name := strings.Join(strings.Fields(after), " "); name != "" {
				res = ForceModelResult{Model: name}
				found = true
				cmdIdx = i
			}
		} else if trimmed == "/unforce-model" || trimmed == "/ufm" {
			res = ForceModelResult{Clear: true}
			found = true
			cmdIdx = i
		}
		break
	}
	if !found {
		return ForceModelResult{}, false, text
	}
	remaining := make([]string, 0, len(lines))
	remaining = append(remaining, lines[:cmdIdx]...)
	remaining = append(remaining, lines[cmdIdx+1:]...)
	bodyStripped := strings.Join(remaining, "\n")
	stripped = strings.TrimSpace(prefix + bodyStripped)
	return res, true, stripped
}

// cutAnyPrefix returns text with the first matching prefix removed. Prefix
// order matters only for overlapping prefixes; the command forms used here
// are disjoint.
func cutAnyPrefix(text string, prefixes ...string) (after string, ok bool) {
	for _, p := range prefixes {
		if after, ok = strings.CutPrefix(text, p); ok {
			return after, true
		}
	}
	return text, false
}

// leadingInjectedPrefixEnd returns the byte offset after leading whitespace
// and complete <tag>...</tag> blocks. Only simple attribute-free tag names are
// recognized, so pasted XML/HTML containing a stray /force-model line can't
// satisfy the guard; unclosed or attribute-bearing tags stop the scan.
func leadingInjectedPrefixEnd(text string) int {
	i := 0
	for i < len(text) {
		// Skip whitespace between blocks.
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
		// Parse the opening tag name.
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
