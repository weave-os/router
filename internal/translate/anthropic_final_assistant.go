package translate

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func prepareAnthropicMessageTail(body []byte, supportsPrefill bool) ([]byte, error) {
	body, err := placeAnthropicSystemMessagesBytes(body)
	if err != nil {
		return nil, fmt.Errorf("place anthropic system messages: %w", err)
	}
	body, err = normalizeAnthropicFinalAssistant(body)
	if err != nil {
		return nil, fmt.Errorf("normalize anthropic final assistant: %w", err)
	}
	if supportsPrefill {
		return body, nil
	}
	body, err = dropAnthropicFinalAssistant(body)
	if err != nil {
		return nil, fmt.Errorf("drop anthropic final assistant: %w", err)
	}
	return body, nil
}

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

func dropAnthropicFinalAssistant(body []byte) ([]byte, error) {
	for {
		messages := gjson.GetBytes(body, "messages").Array()
		last := len(messages) - 1
		if last < 1 || EscalationRole(messages[last].Get("role").String()) != EscalationRoleAssistant {
			return body, nil
		}
		var err error
		body, err = sjson.DeleteBytes(body, fmt.Sprintf("messages.%d", last))
		if err != nil {
			return nil, err
		}
	}
}
