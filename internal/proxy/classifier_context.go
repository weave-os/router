package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"
)

const classifierHistoryResponses = 10

// classifierContextAtUserBoundary preserves V3's user-turn supervision: tool
// continuations reuse that turn's input; their responses become history only
// when the next human message arrives. Never use the clipped policy wire here.
func classifierContextAtUserBoundary(observation translate.EscalationObservation) (router.ClassifierContext, error) {
	if !observation.HistoryComplete || observation.ContinuationID != "" || len(observation.ItemReferenceIDs) != 0 {
		return router.ClassifierContext{}, router.ErrClassifierHistoryUnavailable
	}
	var classifierContext router.ClassifierContext
	features := router.ClassifierFeatures{}
	history := make([]router.ClassifierResponse, 0, classifierHistoryResponses)
	completedResponses := 0
	// Retain only digests in the rolling identity; neither tool arguments nor
	// results enter the model input. Framing comes from encoding/json.
	prefix := ""
	toolCallIDs := make(map[string]bool)
	resolvedToolCallIDs := make(map[string]bool)
	for _, message := range observation.Messages {
		if message.Role == translate.EscalationRoleSystem || message.Role == translate.EscalationRoleDeveloper {
			continue
		}
		userText := make([]string, 0)
		hasToolResult := false
		for _, block := range message.Blocks {
			if block.Type == translate.EscalationBlockToolResult {
				hasToolResult = true
			}
			if block.Type == translate.EscalationBlockText && message.Role == translate.EscalationRoleUser {
				userText = append(userText, block.Text)
			}
		}
		// Mixed tool-result/user-text messages need an explicit client boundary;
		// guessing would turn harness reminders into new human messages.
		if hasToolResult && len(userText) > 0 {
			return router.ClassifierContext{}, fmt.Errorf("ambiguous mixed user/tool boundary: %w", router.ErrClassifierHistoryUnavailable)
		}
		if message.Role == translate.EscalationRoleUser && !hasToolResult {
			features.UserMessageCount++
			currentUserMessage := strings.Join(userText, "\n\n")
			encoded, _ := json.Marshal(struct {
				Prefix   string
				User     string
				Features router.ClassifierFeatures
			}{prefix, currentUserMessage, features})
			digest := sha256.Sum256(encoded)
			classifierContext = router.ClassifierContext{
				TurnKey: hex.EncodeToString(digest[:]), CurrentUserMessage: currentUserMessage,
				PrecedingResponses:     append([]router.ClassifierResponse{}, history...),
				CompletedResponseCount: completedResponses, Features: features,
			}
		}
		if classifierContext.TurnKey == "" {
			return router.ClassifierContext{}, fmt.Errorf("history starts before its human request: %w", router.ErrClassifierHistoryUnavailable)
		}
		for _, block := range message.Blocks {
			switch block.Type {
			case translate.EscalationBlockText:
				if message.Role == translate.EscalationRoleAssistant {
					history = append(history, router.ClassifierResponse{ResponseIndex: completedResponses, Content: block.Text, TurnKey: classifierContext.TurnKey})
					completedResponses++
					if len(history) > classifierHistoryResponses {
						history = history[1:]
					}
				}
			case translate.EscalationBlockToolCall:
				if message.Role != translate.EscalationRoleAssistant || block.ID == "" || toolCallIDs[block.ID] {
					return router.ClassifierContext{}, fmt.Errorf("missing or repeated tool identity: %w", router.ErrClassifierHistoryUnavailable)
				}
				toolCallIDs[block.ID] = true
				features.ToolCallCount++
			case translate.EscalationBlockToolResult:
				if (message.Role != translate.EscalationRoleUser && message.Role != translate.EscalationRoleTool) || !toolCallIDs[block.CallID] || resolvedToolCallIDs[block.CallID] {
					return router.ClassifierContext{}, fmt.Errorf("unmatched or repeated tool result: %w", router.ErrClassifierHistoryUnavailable)
				}
				resolvedToolCallIDs[block.CallID] = true
				if block.IsError != nil && *block.IsError {
					features.ToolErrorCount++
				}
			default:
				return router.ClassifierContext{}, fmt.Errorf("unsupported classifier event: %w", router.ErrClassifierHistoryUnavailable)
			}
		}
		encoded, _ := json.Marshal(struct {
			Prefix  string
			Message translate.EscalationMessage
		}{prefix, message})
		digest := sha256.Sum256(encoded)
		prefix = hex.EncodeToString(digest[:])
	}
	if classifierContext.TurnKey == "" {
		return router.ClassifierContext{}, router.ErrClassifierHistoryUnavailable
	}
	return classifierContext, nil
}
