package policyclient

import (
	"encoding/json"
	"errors"

	"weave-os/router/internal/router/policy"
)

type classifierRequestV5 struct {
	SchemaVersion         string                     `json:"schema_version"`
	ClassifierSelectionID string                     `json:"classifier_selection_id"`
	ClassifierReleaseID   string                     `json:"classifier_release_id"`
	ClassifierVariantID   policy.ClassifierVariantID `json:"classifier_variant_id"`
	RequestID             string                     `json:"request_id"`
	SessionID             string                     `json:"session_id,omitempty"`
	Harness               string                     `json:"harness"`
	PromptText            string                     `json:"prompt_text,omitempty"`
	LatestUserText        string                     `json:"latest_user_text,omitempty"`
	TurnIndex             int                        `json:"turn_index"`
	IsSubagent            bool                       `json:"is_subagent"`
	VisibleTurnIndex      *int                       `json:"visible_turn_index,omitempty"`
	SessionTurnCount      *int                       `json:"session_turn_count,omitempty"`
	TurnType              string                     `json:"turn_type,omitempty"`
	CacheState            string                     `json:"cache_state,omitempty"`
	PriorOutputTokens     *int                       `json:"prior_output_tokens,omitempty"`
	SessionEverSwitched   *bool                      `json:"session_ever_switched,omitempty"`
	HistoryTruncated      *bool                      `json:"history_truncated,omitempty"`
	ConversationMessages  []routeMessage             `json:"conversation_messages,omitempty"`
	OmittedUserTurnCount  *int                       `json:"omitted_user_turn_count,omitempty"`
	AvailableTools        []string                   `json:"available_tools,omitempty"`
	InvokedTools          []string                   `json:"invoked_tools,omitempty"`
	Tools                 []routeTool                `json:"tools,omitempty"`
	EstimatedInputTokens  int                        `json:"estimated_input_tokens"`
	HasTools              bool                       `json:"has_tools"`
	HasImages             bool                       `json:"has_images"`
}

func marshalClassifierRequestV5(query policy.Query) ([]byte, error) {
	if query.SchemaVersion != policy.SchemaVersionV5 || !validClassifierSHA256(query.ClassifierReleaseID) ||
		!validClassifierSHA256(query.ClassifierSelectionID) || !validClassifierVariantID(query.ClassifierVariantID) {
		return nil, errors.New("v5 classifier request requires API, release, selection, and variant identities")
	}
	messageLimits := classifierRouteMessageLimits(query)
	messages := convertRouteMessages(query.ConversationMessages, messageLimits)
	for i := range messages {
		if messages[i].Role == "developer" {
			messages[i].Role = "system"
		}
	}
	turnIndex := turnIndex(messages)
	var visibleTurnIndex *int
	var sessionTurnCount *int
	var sessionEverSwitched *bool
	var historyTruncated *bool
	var turnType string
	var cacheState string
	var priorOutputTokens *int
	if query.TurnContext != nil {
		turnIndex = query.TurnContext.VisibleTurnIndex
		visibleTurnIndex = pointerTo(query.TurnContext.VisibleTurnIndex)
		sessionTurnCount = pointerTo(query.TurnContext.SessionTurnCount)
		sessionEverSwitched = pointerTo(query.TurnContext.SessionEverSwitched)
		historyTruncated = pointerTo(query.TurnContext.HistoryTruncated || routeMessagesExceedLimits(query.ConversationMessages, messageLimits))
		turnType = query.TurnContext.TurnType
		cacheState = query.TurnContext.CacheState
		priorOutputTokens = query.TurnContext.PriorOutputTokens
	}
	var omittedUserTurnCount *int
	if query.InteractionStateInputs {
		omittedUserTurnCount = pointerTo(userTextTurnCount(query.ConversationMessages) - routedUserTextTurnCount(messages))
	}
	invokedTools := clipRouteValues(invokedToolNames(query.ConversationMessages))
	return json.Marshal(classifierRequestV5{
		SchemaVersion: string(policy.SchemaVersionV5), ClassifierSelectionID: query.ClassifierSelectionID,
		ClassifierReleaseID: query.ClassifierReleaseID, ClassifierVariantID: query.ClassifierVariantID,
		RequestID: query.RouteID, SessionID: query.ClientSessionID, Harness: policy.HarnessForClientApp(query.ClientApp),
		PromptText: query.PromptText, LatestUserText: latestUserText(messages), TurnIndex: turnIndex,
		IsSubagent: turnType == "sub_agent_dispatch", VisibleTurnIndex: visibleTurnIndex,
		SessionTurnCount: sessionTurnCount, TurnType: turnType, CacheState: cacheState,
		PriorOutputTokens: priorOutputTokens, SessionEverSwitched: sessionEverSwitched,
		HistoryTruncated: historyTruncated, ConversationMessages: messages,
		OmittedUserTurnCount: omittedUserTurnCount,
		AvailableTools:       clipRouteValues(unionToolNames(query.AvailableTools, invokedTools)),
		InvokedTools:         invokedTools, Tools: routeTools(query.Tools), EstimatedInputTokens: query.EstimatedInputTokens,
		HasTools: query.HasTools, HasImages: query.HasImages,
	})
}
