package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"
)

const classifierHistoryResponseLimit = 10

const classifierClaudeToolHint = "First privately list what you need next; then request every item that doesn't depend on another's result in this one response."

var classifierClaudeTokenBudget = regexp.MustCompile(`(^|\n\n)<total_tokens>[0-9]+ tokens left</total_tokens>(\n\nUSD budget: \$[0-9]+(\.[0-9]+)?/\$[0-9]+(\.[0-9]+)?; \$[0-9]+(\.[0-9]+)? remaining)?$`)

// classifierContextForCall uses the complete causal prefix of this invocation,
// not the clipped policy wire or a snapshot at the last human message.
func classifierContextForCall(observation translate.EscalationObservation) (router.ClassifierContext, error) {
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
	prefixDigests := make([]string, 0, len(observation.Messages))
	toolCallIDs := make(map[string]bool)
	resolvedToolCallIDs := make(map[string]bool)
	for index, message := range observation.Messages {
		boundaryOpen := classifierContext.AtUserBoundary
		if message.HasOmittedMedia {
			return router.ClassifierContext{}, fmt.Errorf("media identity unavailable: %w", router.ErrClassifierHistoryUnavailable)
		}
		// Claude appends this exact per-invocation hint after its persistent
		// token-budget message, then omits it on replay. Do not generalize this
		// to arbitrary instructions or alter the provider's original request.
		if index == len(observation.Messages)-1 && message.Role == translate.EscalationRoleSystem && len(message.Blocks) == 2 &&
			message.Blocks[0].Type == translate.EscalationBlockText && classifierClaudeTokenBudget.MatchString(message.Blocks[0].Text) &&
			message.Blocks[1].Type == translate.EscalationBlockText && message.Blocks[1].Text == classifierClaudeToolHint {
			message.Blocks = message.Blocks[:1]
		}
		identityMessage := classifierReplayMessage(message, index)
		priorPrefix := prefix
		encodedPrefixMessage, _ := json.Marshal(struct {
			Prefix  string
			Message translate.EscalationMessage
		}{prefix, identityMessage})
		prefixDigest := sha256.Sum256(encodedPrefixMessage)
		prefix = hex.EncodeToString(prefixDigest[:])
		prefixDigests = append(prefixDigests, prefix)
		if message.Role == translate.EscalationRoleSystem || message.Role == translate.EscalationRoleDeveloper {
			continue
		}
		classifierContext.AtUserBoundary = false
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
			features.UserMessageCount++
			currentUserMessage := strings.Join(userText, "\n\n")
			if boundaryOpen {
				// Setup context and the actual prompt can be separate user
				// messages in one call. Neither has a response/prediction yet.
				currentUserMessage = classifierContext.CurrentUserMessage + "\n\n" + currentUserMessage
			}
			classifierContext.CurrentUserMessage = currentUserMessage
			classifierContext.AtUserBoundary = true
		}
		if features.UserMessageCount == 0 {
			return router.ClassifierContext{}, fmt.Errorf("history starts before its human request: %w", router.ErrClassifierHistoryUnavailable)
		}
		if message.Role == translate.EscalationRoleAssistant && !classifierContext.HasAssistantHistory {
			classifierContext.RootTurnDigest = priorPrefix
			classifierContext.HasAssistantHistory = true
		}
		for _, block := range message.Blocks {
			switch block.Type {
			case translate.EscalationBlockText:
				if message.Role == translate.EscalationRoleAssistant {
					responseHistory = append(responseHistory, router.ClassifierResponse{ResponseIndex: completedResponses, MessageIndex: index, UserMessageCount: features.UserMessageCount, Content: block.Text, PrefixDigest: priorPrefix})
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
	if features.UserMessageCount == 0 || len(toolCallIDs) != len(resolvedToolCallIDs) {
		return router.ClassifierContext{}, router.ErrClassifierHistoryUnavailable
	}
	classifierContext.PrefixDigests = prefixDigests
	classifierContext.TurnDigest = prefix
	if !classifierContext.HasAssistantHistory {
		classifierContext.RootTurnDigest = prefix
	}
	classifierContext.Features = features
	classifierContext.CompletedResponseCount = completedResponses
	classifierContext.PrecedingResponses = responseHistory
	return classifierContext, nil
}
