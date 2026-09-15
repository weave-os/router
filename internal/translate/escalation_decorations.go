package translate

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
