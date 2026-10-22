package translate

import "strings"

// WithoutEscalationDecorations copies visible messages without router-authored text.
func WithoutEscalationDecorations(messages []EscalationMessage) []EscalationMessage {
	cleaned := make([]EscalationMessage, 0, len(messages))
	for _, message := range messages {
		blocks := make([]EscalationBlock, 0, len(message.Blocks))
		for _, block := range message.Blocks {
			if block.Type == EscalationBlockText {
				block.Text = routingMarkerPattern.ReplaceAllString(block.Text, "")
				block.Text = feedbackFooterPattern.ReplaceAllString(block.Text, "")
				if block.Text == "" {
					continue
				}
			}
			blocks = append(blocks, block)
		}
		message.Blocks = blocks
		if len(blocks) != 0 {
			cleaned = append(cleaned, message)
		}
	}
	return cleaned
}

// WithoutLeadingClientInjectedText removes known client-authored wrapper
// blocks while preserving any human text that follows them.
func WithoutLeadingClientInjectedText(text string) string {
	remainder := text
	stripped := false
	for {
		trimmed := strings.TrimLeft(remainder, " \t\r\n")
		if !isClaudeCodeInjectedBlock(trimmed) {
			if stripped {
				return strings.TrimSpace(remainder)
			}
			return remainder
		}
		blockEnd := leadingClaudeCodeInjectedBlockEnd(trimmed)
		if blockEnd == 0 {
			return remainder
		}
		remainder = trimmed[blockEnd:]
		stripped = true
	}
}

// WithDeveloperRolesAsSystem preserves the established XGBoost observation
// vocabulary while other consumers retain role fidelity.
func WithDeveloperRolesAsSystem(messages []EscalationMessage) []EscalationMessage {
	normalized := append([]EscalationMessage(nil), messages...)
	for index := range normalized {
		if normalized[index].Role == EscalationRoleDeveloper {
			normalized[index].Role = EscalationRoleSystem
		}
	}
	return normalized
}
