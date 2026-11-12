package translate

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Clients replay the full conversation every turn, and a router directive the
// user typed on an earlier turn (/fm, $router-session, …) comes back as an
// ordinary user message. The directive was already applied when it was the
// trailing turn and its ack stripped on replay, so what remains is a dangling
// command the model reads, reasons about, and sometimes re-runs. These passes
// remove it from history. The trailing turn is never touched: that is where
// the Extract*Command readers still have to find a live directive. The first
// user message is stripped like any other; the session key hashes the copy
// StripHistoricalCommands captures first (ThreadDiscriminatorText), so the pin
// an opening directive created survives.

// stripHistoricalDirective returns text with any leading router directive
// removed, reporting whether one was found. Any text the user wrote alongside
// the directive is kept.
func stripHistoricalDirective(text string) (stripped string, found bool) {
	for _, directive := range routerDirectives {
		if ok, rest := directive.parse(text); ok {
			return rest, true
		}
	}
	return text, false
}

// StripHistoricalCommands removes router directives from every user message
// before the trailing user turn. The returned count is the number of messages
// edited or dropped.
func (env *RequestEnvelope) StripHistoricalCommands() (int, error) {
	switch env.format {
	case FormatAnthropic, FormatOpenAI:
	default:
		return 0, nil
	}
	msgs := gjson.GetBytes(env.body, "messages")
	if !msgs.IsArray() {
		return 0, nil
	}
	env.captureThreadDiscriminator()
	messages := msgs.Array()
	lastUserIdx := trailingUserTurnIndex(messages, func(msg gjson.Result) bool {
		return msg.Get("role").String() == "user"
	})

	changed := 0
	rebuilt := make([]string, 0, len(messages))
	dropAttachment := false
	var skillExec skillExecTracker
	keptUser, droppedUser := false, false
	for i, msg := range messages {
		if dropAttachment {
			dropAttachment = false
			if isSkillAttachment(messages, i) {
				skillExec.path = skillEmitPath(msg.Get("content"))
				changed++
				continue
			}
		}
		role := msg.Get("role").String()
		if role == "user" && !isSkillAttachment(messages, i) && classifyLastMessageGJSON(role, msg.Get("content")) == "user_prompt" {
			skillExec.path = ""
		}
		raw, edited, keep, err := stripHistoricalSkillToolMessage(msg, &skillExec)
		if err != nil {
			return 0, err
		}
		if edited {
			changed++
			if !keep {
				continue
			}
			msg = gjson.Parse(raw)
		}
		// Dropping an opening directive must not leave an orphaned assistant ack.
		if role == "assistant" && droppedUser && !keptUser && !carriesToolCall(msg) {
			changed++
			continue
		}
		if role != "user" || i == lastUserIdx || isSkillAttachment(messages, i) {
			keptUser = keptUser || role == "user"
			rebuilt = append(rebuilt, msg.Raw)
			continue
		}
		raw, edited, keep, err = stripDirectiveFromMessage(msg, "text")
		if err != nil {
			return 0, err
		}
		if !edited {
			keptUser = true
			rebuilt = append(rebuilt, msg.Raw)
			continue
		}
		changed++
		dropAttachment = true
		if !keep {
			droppedUser = true
			continue
		}
		keptUser = true
		rebuilt = append(rebuilt, raw)
	}
	if changed == 0 {
		return 0, nil
	}
	return env.setMessages(rebuilt, changed), nil
}

// trailingUserTurnIndex finds the user message that owns the current turn,
// looking past the skill attachment Codex appends after a typed directive.
func trailingUserTurnIndex(messages []gjson.Result, isUser func(gjson.Result) bool) int {
	for i := len(messages) - 1; i >= 0; i-- {
		if !isUser(messages[i]) || isSkillAttachment(messages, i) {
			continue
		}
		return i
	}
	return -1
}

func carriesToolCall(msg gjson.Result) bool {
	if msg.Get("tool_calls.#").Int() > 0 {
		return true
	}
	for _, part := range msg.Get("content").Array() {
		if part.Get("type").String() == "tool_use" {
			return true
		}
	}
	return false
}

func skillEmitPath(content gjson.Result) string {
	text := content.Str
	if content.IsArray() {
		for _, part := range content.Array() {
			text += part.Get("text").Str
		}
	}
	_, rest, found := strings.Cut(text, "<path>")
	if !found {
		return ""
	}
	skillPath, _, found := strings.Cut(rest, "</path>")
	if !found || !strings.HasSuffix(strings.TrimSpace(skillPath), "/SKILL.md") {
		return ""
	}
	return strings.TrimSuffix(strings.TrimSpace(skillPath), "/SKILL.md") + "/scripts/emit.sh"
}

type skillExecTracker struct {
	path           string
	droppedCallIDs map[string]struct{}
}

func (tracker *skillExecTracker) dropCall(id, name, code string) bool {
	if tracker.path != "" && name == "exec" && id != "" && strings.Contains(code, tracker.path) {
		if tracker.droppedCallIDs == nil {
			tracker.droppedCallIDs = make(map[string]struct{})
		}
		tracker.droppedCallIDs[id] = struct{}{}
		return true
	}
	delete(tracker.droppedCallIDs, id)
	return false
}

func (tracker *skillExecTracker) isDroppedOutput(id string) bool {
	_, dropped := tracker.droppedCallIDs[id]
	return dropped
}

func stripHistoricalSkillToolMessage(msg gjson.Result, tracker *skillExecTracker) (raw string, edited, keep bool, err error) {
	if tracker.path == "" && len(tracker.droppedCallIDs) == 0 {
		return msg.Raw, false, true, nil
	}
	if msg.Get("role").Str == "tool" {
		if tracker.isDroppedOutput(msg.Get("tool_call_id").Str) {
			return "", true, false, nil
		}
	}
	raw = msg.Raw
	for _, field := range []string{"tool_calls", "content"} {
		parts := msg.Get(field)
		if !parts.IsArray() {
			continue
		}
		kept := make([]string, 0, len(parts.Array()))
		for _, part := range parts.Array() {
			id, name := part.Get("id").Str, part.Get("function.name").Str
			code := part.Get("function.arguments").Str
			isCall := field == "tool_calls" || part.Get("type").Str == "tool_use"
			if field == "content" {
				name = ""
				if part.Get("type").Str == "tool_use" {
					name = part.Get("name").Str
					code = part.Get("input").Raw
				}
			}
			if isCall && msg.Get("role").Str == "assistant" && tracker.dropCall(id, name, code) {
				edited = true
				continue
			}
			if part.Get("type").Str == "tool_result" {
				if tracker.isDroppedOutput(part.Get("tool_use_id").Str) {
					edited = true
					continue
				}
			}
			kept = append(kept, part.Raw)
		}
		if len(kept) != len(parts.Array()) {
			raw, err = sjson.SetRaw(raw, field, "["+strings.Join(kept, ",")+"]")
			if err != nil {
				return "", false, false, fmt.Errorf("strip historical skill tools: %w", err)
			}
		}
	}
	if !edited {
		return raw, false, true, nil
	}
	rebuilt := gjson.Parse(raw)
	content := rebuilt.Get("content")
	keep = carriesToolCall(rebuilt) || (content.IsArray() && len(content.Array()) > 0) || (content.Type == gjson.String && strings.TrimSpace(content.Str) != "")
	return raw, true, keep, nil
}

// stripDirectiveFromMessage rewrites a user message's content with its
// leading directive removed. keep is false when nothing user-authored
// remains and the whole message should go.
func stripDirectiveFromMessage(msg gjson.Result, textType string) (raw string, edited, keep bool, err error) {
	content := msg.Get("content")
	switch {
	case content.Type == gjson.String:
		stripped, found := stripHistoricalDirective(content.String())
		if !found {
			return msg.Raw, false, true, nil
		}
		if isOnlyKnownInjectedText(stripped) {
			return "", true, false, nil
		}
		out, err := sjson.Set(msg.Raw, "content", stripped)
		if err != nil {
			return "", false, false, fmt.Errorf("strip historical router command from message content: %w", err)
		}
		return out, true, true, nil
	case content.IsArray():
		parts := content.Array()
		partRaws := make([]string, 0, len(parts))
		found := false
		hasBody := false
		for _, part := range parts {
			if part.Get("type").String() != textType {
				hasBody = true
				partRaws = append(partRaws, part.Raw)
				continue
			}
			text := part.Get("text").String()
			stripped, ok := stripHistoricalDirective(text)
			if !ok {
				if !isOnlyKnownInjectedText(text) {
					hasBody = true
				}
				partRaws = append(partRaws, part.Raw)
				continue
			}
			found = true
			if isOnlyKnownInjectedText(stripped) {
				continue
			}
			hasBody = true
			out, err := sjson.Set(part.Raw, "text", stripped)
			if err != nil {
				return "", false, false, fmt.Errorf("strip historical router command from content part: %w", err)
			}
			partRaws = append(partRaws, out)
		}
		if !found {
			return msg.Raw, false, true, nil
		}
		if !hasBody {
			return "", true, false, nil
		}
		out, err := sjson.SetRaw(msg.Raw, "content", "["+strings.Join(partRaws, ",")+"]")
		if err != nil {
			return "", false, false, fmt.Errorf("rebuild message content after historical router command strip: %w", err)
		}
		return out, true, true, nil
	default:
		return msg.Raw, false, true, nil
	}
}

// StripHistoricalCommandsFromResponsesInput is the Responses-shape twin of
// StripHistoricalCommands for the native body Codex dispatches verbatim.
func StripHistoricalCommandsFromResponsesInput(body []byte) ([]byte, error) {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body, nil
	}
	items := input.Array()
	lastUserIdx := trailingUserTurnIndex(items, responsesUserMessage)

	rebuilt := make([]string, 0, len(items))
	changed := false
	dropAttachment := false
	var skillExec skillExecTracker
	for i, item := range items {
		if dropAttachment {
			dropAttachment = false
			if isSkillAttachment(items, i) {
				skillExec.path = skillEmitPath(item.Get("content"))
				changed = true
				continue
			}
		}
		if responsesUserMessage(item) && !isSkillAttachment(items, i) {
			skillExec.path = ""
		}
		itemType := item.Get("type").Str
		if itemType == "function_call" || itemType == "custom_tool_call" {
			code := item.Get("arguments").Str + item.Get("input").Str
			if skillExec.dropCall(item.Get("call_id").Str, item.Get("name").Str, code) {
				changed = true
				continue
			}
		}
		if itemType == "function_call_output" || itemType == "custom_tool_call_output" {
			if skillExec.isDroppedOutput(item.Get("call_id").Str) {
				continue
			}
		}
		if !responsesUserMessage(item) || i == lastUserIdx || isSkillAttachment(items, i) {
			rebuilt = append(rebuilt, item.Raw)
			continue
		}
		raw, edited, keep, err := stripDirectiveFromMessage(item, "input_text")
		if err != nil {
			return nil, err
		}
		if !edited {
			rebuilt = append(rebuilt, item.Raw)
			continue
		}
		changed = true
		dropAttachment = true
		if !keep {
			continue
		}
		rebuilt = append(rebuilt, raw)
	}
	if !changed {
		return body, nil
	}
	out, err := sjson.SetRawBytes(body, "input", []byte("["+strings.Join(rebuilt, ",")+"]"))
	if err != nil {
		return nil, fmt.Errorf("strip historical router commands from Responses input: %w", err)
	}
	return out, nil
}

func responsesUserMessage(item gjson.Result) bool {
	itemType := item.Get("type").Str
	if itemType != "message" && itemType != "" {
		return false
	}
	return item.Get("role").Str == "user"
}
