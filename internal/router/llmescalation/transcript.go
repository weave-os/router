// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
// Adapted from Switchyard's escalation.rs at SwitchyardRevision.
package llmescalation

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"weave-os/router/internal/translate"
)

// InstructionFingerprint changes for new top-level user instructions, including
// repeated identical text, but not for Anthropic's user-role tool results.
func InstructionFingerprint(messages []translate.EscalationMessage) [32]byte {
	count := 0
	latest := ""
	for _, message := range messages {
		if message.Role != translate.EscalationRoleUser {
			continue
		}
		parts := make([]string, 0)
		for _, block := range message.Blocks {
			if block.Type == translate.EscalationBlockText {
				instructionText := translate.WithoutLeadingClientInjectedText(block.Text)
				if strings.TrimSpace(instructionText) != "" {
					parts = append(parts, instructionText)
				}
			}
		}
		if len(parts) > 0 {
			count++
			latest = strings.Join(parts, "\n")
		}
	}
	return sha256.Sum256([]byte(fmt.Sprintf("%d:%s", count, latest)))
}

const trimMarker = " ...[trimmed] "
const truncationSuffix = "...<truncated>"
const maxTranscriptChars = 18000

// RenderTranscript ports Switchyard's Unicode-aware anchor/window condenser.
func RenderTranscript(messages []translate.EscalationMessage) string {
	anchors := make([]string, 0)
	window := make([]string, 0)
	assistantSeen := false
	turn := 0
	for _, message := range messages {
		text := messageText(message)
		switch {
		case message.Role == translate.EscalationRoleSystem || message.Role == translate.EscalationRoleDeveloper:
			anchors = append(anchors, fmt.Sprintf("[%s] %s", message.Role, truncateMiddle(text, 1000)))
		case message.Role == translate.EscalationRoleUser && !assistantSeen:
			anchors = append(anchors, "[user (task)] "+truncateMiddle(text, 4000))
		default:
			if message.Role == translate.EscalationRoleAssistant {
				assistantSeen = true
				turn++
			}
			window = append(window, fmt.Sprintf("[%s] %s", message.Role, truncateMiddle(text, 500)))
		}
	}
	if len(window) > 28 {
		window = window[len(window)-28:]
	}
	assemble := func() string {
		lines := []string{fmt.Sprintf("Conversation turn %d; showing the last %d of %d messages after the task framing.", turn, len(window), len(messages))}
		lines = append(lines, anchors...)
		lines = append(lines, window...)
		return strings.Join(lines, "\n")
	}
	transcript := assemble()
	for len([]rune(transcript)) > maxTranscriptChars && len(window) > 0 {
		window = window[1:]
		transcript = assemble()
	}
	if runes := []rune(transcript); len(runes) > maxTranscriptChars {
		transcript = string(runes[:maxTranscriptChars-len([]rune(truncationSuffix))-1]) + truncationSuffix
	}
	return transcript
}

func truncateMiddle(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	keep := min(max(limit-len([]rune(trimMarker)), 20), len(runes))
	head := keep * 2 / 3
	return string(runes[:head]) + trimMarker + string(runes[len(runes)-(keep-head):])
}

func messageText(message translate.EscalationMessage) string {
	parts := make([]string, 0, len(message.Blocks))
	for _, block := range message.Blocks {
		switch block.Type {
		case translate.EscalationBlockText:
			parts = append(parts, block.Text)
		case translate.EscalationBlockToolCall:
			arguments := block.ArgumentsJSON
			var compact bytes.Buffer
			if json.Compact(&compact, []byte(arguments)) == nil {
				arguments = compact.String()
			}
			parts = append(parts, fmt.Sprintf("tool_call %s(%s)", block.Name, arguments))
		case translate.EscalationBlockToolResult:
			parts = append(parts, toolResultText(block.ContentJSON)...)
		}
	}
	return strings.Join(parts, " ")
}

func toolResultText(encoded string) []string {
	var text string
	if json.Unmarshal([]byte(encoded), &text) == nil {
		return []string{text}
	}
	var blocks []struct {
		Type    translate.EscalationBlockType `json:"type"`
		Text    string                        `json:"text"`
		Content json.RawMessage               `json:"content"`
	}
	if json.Unmarshal([]byte(encoded), &blocks) != nil {
		return []string{encoded}
	}
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Type == translate.EscalationBlockText {
			parts = append(parts, block.Text)
		}
		if block.Type == translate.EscalationBlockToolResult {
			parts = append(parts, toolResultText(string(block.Content))...)
		}
	}
	return parts
}
