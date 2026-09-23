package router

import (
	"errors"
	"fmt"
)

// ClassifierComplexity is the four-class ordinal vocabulary of the V3 model.
type ClassifierComplexity int

const (
	ClassifierLow ClassifierComplexity = iota
	ClassifierMedium
	ClassifierHigh
	ClassifierMaximum
)

// ClassifierHistorySource distinguishes predictions from training labels.
type ClassifierHistorySource string

const ClassifierHistoricalPrediction ClassifierHistorySource = "historical_prediction"

// ErrClassifierHistoryUnavailable prevents partial or unlabeled history from
// masquerading as the complete causal prefix expected by the classifier.
var ErrClassifierHistoryUnavailable = errors.New("classifier history unavailable")

// ClassifierFeatures are whole-prefix counts at an API-call boundary, not
// counts in the ten-response window. Unknown tool outcomes are not successes.
type ClassifierFeatures struct {
	UserMessageCount int `json:"user_message_count"`
	ToolCallCount    int `json:"tool_call_count"`
	ToolErrorCount   int `json:"tool_error_count"`
}

// ClassifierResponse locates one text block in the causal prefix. Several
// messages/blocks can belong to one invocation. Empty text blocks count.
type ClassifierResponse struct {
	ResponseIndex    int
	MessageIndex     int
	UserMessageCount int
	Content          string
	PrefixDigest     string
}

// ClassifierContext is the untruncated V3 input before historical predictions
// are joined. TurnDigest is the complete API-call prefix digest (the name is
// retained for persisted compatibility), identical only for retries of that input.
type ClassifierContext struct {
	PrefixDigests          []string
	TurnDigest             string
	RootTurnDigest         string
	HasAssistantHistory    bool
	AtUserBoundary         bool
	CurrentUserMessage     string
	PrecedingResponses     []ClassifierResponse
	CompletedResponseCount int
	Features               ClassifierFeatures
}

// AtomicClassificationRequest is the V3 /classify contract. Provenance indices
// are validated by the service but omitted from the rendered model prompt.
type AtomicClassificationRequest struct {
	User                   AtomicClassifierUser    `json:"user"`
	HistorySource          ClassifierHistorySource `json:"history_source"`
	CompletedResponseCount int                     `json:"completed_response_count"`
}

// AtomicClassifierUser contains only the three feature groups used in training.
type AtomicClassifierUser struct {
	CurrentUserMessage string                        `json:"current_user_message"`
	PrecedingResponses []PredictedClassifierResponse `json:"preceding_agent_responses"`
	Features           ClassifierFeatures            `json:"conversation_features"`
}

// PredictedClassifierResponse uses the prediction made at its owning boundary,
// never a label reconstructed from response text or from a selected model tier.
type PredictedClassifierResponse struct {
	ResponseIndex int                  `json:"response_index"`
	Content       string               `json:"content"`
	Complexity    ClassifierComplexity `json:"complexity"`
}

// WithHistoricalPredictions joins the exact ten-response suffix to previously
// recorded predictions. The caller must look them up within the same tenant,
// thread and immutable classifier release. Missing history is never defaulted.
func (c ClassifierContext) WithHistoricalPredictions(predictions map[string]ClassifierComplexity) (AtomicClassificationRequest, error) {
	features := c.Features
	if c.TurnDigest == "" || features.UserMessageCount < 1 || features.ToolCallCount < 0 || features.ToolErrorCount < 0 || features.ToolErrorCount > features.ToolCallCount || c.CompletedResponseCount < 0 {
		return AtomicClassificationRequest{}, fmt.Errorf("invalid causal classifier features: %w", ErrClassifierHistoryUnavailable)
	}
	firstResponseIndex := max(0, c.CompletedResponseCount-10)
	if len(c.PrecedingResponses) != c.CompletedResponseCount-firstResponseIndex {
		return AtomicClassificationRequest{}, fmt.Errorf("incomplete response suffix: %w", ErrClassifierHistoryUnavailable)
	}
	responses := make([]PredictedClassifierResponse, 0, len(c.PrecedingResponses))
	for offset, response := range c.PrecedingResponses {
		complexity, exists := predictions[response.PrefixDigest]
		if response.ResponseIndex != firstResponseIndex+offset || response.PrefixDigest == "" || response.PrefixDigest == c.TurnDigest || !exists || complexity < ClassifierLow || complexity > ClassifierMaximum {
			return AtomicClassificationRequest{}, fmt.Errorf("missing or invalid historical prediction at response %d: %w", response.ResponseIndex, ErrClassifierHistoryUnavailable)
		}
		responses = append(responses, PredictedClassifierResponse{ResponseIndex: response.ResponseIndex, Content: response.Content, Complexity: complexity})
	}
	return AtomicClassificationRequest{
		User:                   AtomicClassifierUser{CurrentUserMessage: c.CurrentUserMessage, PrecedingResponses: responses, Features: features},
		HistorySource:          ClassifierHistoricalPrediction,
		CompletedResponseCount: c.CompletedResponseCount,
	}, nil
}
