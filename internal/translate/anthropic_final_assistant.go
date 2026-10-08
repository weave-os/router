package translate

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Anthropic treats a final assistant message as a prefill and rejects terminal
// whitespace. Empty text suffixes have no prefill to preserve; non-text blocks
// and earlier history must retain their exact content.
func normalizeAnthropicFinalAssistant(body []byte) ([]byte, error) {
	messages := gjson.GetBytes(body, "messages").Array()
	for messageIndex := len(messages) - 1; messageIndex >= 0; messageIndex-- {
		message := messages[messageIndex]
		if EscalationRole(message.Get("role").String()) != EscalationRoleAssistant {
			return body, nil
		}
		messagePath := fmt.Sprintf("messages.%d", messageIndex)
		content := message.Get("content")
		switch {
		case content.Type == gjson.String:
			trimmed := strings.TrimRightFunc(content.String(), unicode.IsSpace)
			if trimmed != "" {
				if trimmed == content.String() {
					return body, nil
				}
				return sjson.SetBytes(body, messagePath+".content", trimmed)
			}
		case content.IsArray():
			blocks := content.Array()
			for blockIndex := len(blocks) - 1; blockIndex >= 0; blockIndex-- {
				block := blocks[blockIndex]
				if escalationWireType(block.Get("type").String()) != escalationWireText {
					return body, nil
				}
				text := block.Get("text")
				if text.Type != gjson.String {
					return body, nil
				}
				trimmed := strings.TrimRightFunc(text.String(), unicode.IsSpace)
				blockPath := fmt.Sprintf("%s.content.%d", messagePath, blockIndex)
				if trimmed != "" {
					if trimmed == text.String() {
						return body, nil
					}
					return sjson.SetBytes(body, blockPath+".text", trimmed)
				}
				var err error
				body, err = sjson.DeleteBytes(body, blockPath)
				if err != nil {
					return nil, err
				}
			}
		default:
			return body, nil
		}
		var err error
		body, err = sjson.DeleteBytes(body, messagePath)
		if err != nil {
			return nil, err
		}
	}
	return body, nil
}

// OpenAI assistant-ending requests ask for another turn, not an Anthropic
// prefill. New Claude models reject prefills; preserve the history and request
// continuation explicitly. Pending tool calls must still await their results.
func appendAnthropicAssistantContinuation(body []byte) ([]byte, error) {
	messages := gjson.GetBytes(body, "messages").Array()
	if len(messages) == 0 {
		return body, nil
	}
	last := messages[len(messages)-1]
	if EscalationRole(last.Get("role").String()) != EscalationRoleAssistant {
		return body, nil
	}
	content := last.Get("content")
	if content.IsArray() {
		for _, block := range content.Array() {
			if escalationWireType(block.Get("type").String()) != escalationWireText {
				return body, nil
			}
		}
	} else if content.Type != gjson.String {
		return body, nil
	}
	return sjson.SetBytes(body, "messages.-1", map[string]any{
		"role": EscalationRoleUser, "content": "Continue.",
	})
}
