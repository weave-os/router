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

const classifierHistoryResponseLimit = 10

// classifierContextAtUserBoundary preserves V3's user-turn supervision: tool
// continuations reuse that turn's input; their responses become history only
// when the next human message arrives. Never use the clipped policy wire here.
func classifierContextAtUserBoundary(observation translate.EscalationObservation) (router.ClassifierContext, error) {
	if !observation.HistoryComplete || observation.ContinuationID != "" || len(observation.ItemReferenceIDs) != 0 {
		return router.ClassifierContext{}, router.ErrClassifierHistoryUnavailable
	}
	var classifierContext router.ClassifierContext
	features := router.ClassifierFeatures{}
	responseHistory := make([]router.ClassifierResponse, 0, classifierHistoryResponseLimit)
	completedResponses := 0
	// Retain only digests in the rolling identity; neither tool arguments nor
	// results enter the model input. Framing comes from encoding/json.
	prefix := ""
	toolCallIDs := make(map[string]bool)
	resolvedToolCallIDs := make(map[string]bool)
	instructionsAfterBoundary := false
	for _, message := range observation.Messages {
		classifierContext.AtUserBoundary = false
		if message.HasOmittedMedia {
			return router.ClassifierContext{}, fmt.Errorf("media identity unavailable: %w", router.ErrClassifierHistoryUnavailable)
		}
		priorPrefix := prefix
		encodedPrefixMessage, _ := json.Marshal(struct {
			Prefix  string
			Message translate.EscalationMessage
		}{prefix, message})
		prefixDigest := sha256.Sum256(encodedPrefixMessage)
		prefix = hex.EncodeToString(prefixDigest[:])
		if message.Role == translate.EscalationRoleSystem || message.Role == translate.EscalationRoleDeveloper {
			instructionsAfterBoundary = true
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
			if len(userText) == 0 || len(toolCallIDs) != len(resolvedToolCallIDs) {
				return router.ClassifierContext{}, fmt.Errorf("incomplete user boundary: %w", router.ErrClassifierHistoryUnavailable)
			}
			instructionsAfterBoundary = false
			features.UserMessageCount++
			currentUserMessage := strings.Join(userText, "\n\n")
			encodedTurnInput, _ := json.Marshal(struct {
				Prefix   string
				User     string
				Features router.ClassifierFeatures
			}{priorPrefix, currentUserMessage, features})
			turnDigest := sha256.Sum256(encodedTurnInput)
			rootDigest := classifierContext.RootTurnDigest
			if rootDigest == "" {
				rootDigest = hex.EncodeToString(turnDigest[:])
			}
			classifierContext = router.ClassifierContext{
				RootTurnDigest: rootDigest, PreviousTurnDigest: classifierContext.TurnDigest, AtUserBoundary: true,
				TurnDigest: hex.EncodeToString(turnDigest[:]), CurrentUserMessage: currentUserMessage,
				PrecedingResponses:     append([]router.ClassifierResponse{}, responseHistory...),
				CompletedResponseCount: completedResponses, Features: features,
			}
		}
		if classifierContext.TurnDigest == "" {
			return router.ClassifierContext{}, fmt.Errorf("history starts before its human request: %w", router.ErrClassifierHistoryUnavailable)
		}
		if instructionsAfterBoundary {
			return router.ClassifierContext{}, fmt.Errorf("instructions changed within a user turn: %w", router.ErrClassifierHistoryUnavailable)
		}
		for _, block := range message.Blocks {
			switch block.Type {
			case translate.EscalationBlockText:
				if message.Role == translate.EscalationRoleAssistant {
					responseHistory = append(responseHistory, router.ClassifierResponse{ResponseIndex: completedResponses, Content: block.Text, TurnDigest: classifierContext.TurnDigest})
					completedResponses++
					if len(responseHistory) > classifierHistoryResponseLimit {
						responseHistory = responseHistory[1:]
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
	}
	if classifierContext.TurnDigest == "" || instructionsAfterBoundary || len(toolCallIDs) != len(resolvedToolCallIDs) {
		return router.ClassifierContext{}, router.ErrClassifierHistoryUnavailable
	}
	return classifierContext, nil
}
