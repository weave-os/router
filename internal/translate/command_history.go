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
// the Extract*Command readers still have to find a live directive.

// stripHistoricalDirective returns text with any leading router directive
// removed, reporting whether one was found. Any text the user wrote alongside
// the directive is kept.
func stripHistoricalDirective(text string) (stripped string, found bool) {
	if _, ok, rest := parseForceModelCommandFromSource(text, false); ok {
		return rest, true
	}
	if _, ok, rest := parseRouterFeedbackCommand(text); ok {
		return rest, true
	}
	if ok, rest := parseRouterSessionCommand(text); ok {
		return rest, true
	}
	if ok, rest := parseRouterModelsCommand(text); ok {
		return rest, true
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
	messages := msgs.Array()
	lastUserIdx := trailingUserTurnIndex(messages)

	changed := 0
	rebuilt := make([]string, 0, len(messages))
	dropAttachment := false
	for i, msg := range messages {
		if dropAttachment {
			dropAttachment = false
			if isSkillAttachment(messages, i) {
				changed++
				continue
			}
		}
		if msg.Get("role").String() != "user" || i == lastUserIdx || isSkillAttachment(messages, i) {
			rebuilt = append(rebuilt, msg.Raw)
			continue
		}
		raw, edited, keep, err := stripDirectiveFromMessage(msg, "text")
		if err != nil {
			return 0, err
		}
		if !edited {
			rebuilt = append(rebuilt, msg.Raw)
			continue
		}
		changed++
		dropAttachment = true
		if !keep {
			continue
		}
		rebuilt = append(rebuilt, raw)
	}
	if changed == 0 {
		return 0, nil
	}
	return env.setMessages(rebuilt, changed), nil
}

// trailingUserTurnIndex finds the user message that owns the current turn,
// looking past the skill attachment Codex appends after a typed directive.
func trailingUserTurnIndex(messages []gjson.Result) int {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Get("role").String() != "user" || isSkillAttachment(messages, i) {
			continue
		}
		return i
	}
	return -1
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
		if isOnlyInjectedCommandText(stripped) {
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
				if !isOnlyInjectedCommandText(text) {
					hasBody = true
				}
				partRaws = append(partRaws, part.Raw)
				continue
			}
			found = true
			if isOnlyInjectedCommandText(stripped) {
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
	lastUserIdx := -1
	for i := len(items) - 1; i >= 0; i-- {
		if !responsesUserMessage(items[i]) || isSkillAttachment(items, i) {
			continue
		}
		lastUserIdx = i
		break
	}

	rebuilt := make([]string, 0, len(items))
	changed := false
	dropAttachment := false
	for i, item := range items {
		if dropAttachment {
			dropAttachment = false
			if isSkillAttachment(items, i) {
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
