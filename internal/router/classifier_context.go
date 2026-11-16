package router

import (
	"errors"
	"fmt"
)

type ClassifierComplexity int

const (
	ClassifierLow ClassifierComplexity = iota
	ClassifierMedium
	ClassifierHigh
	ClassifierMaximum
)

type ClassifierHistorySource string

const ClassifierHistoricalPrediction ClassifierHistorySource = "historical_prediction"

var ErrClassifierHistoryUnavailable = errors.New("classifier history unavailable")

type ClassifierFeatures struct {
	UserMessageCount int `json:"user_message_count"`
	ToolCallCount    int `json:"tool_call_count"`
	ToolErrorCount   int `json:"tool_error_count"`
}

type ClassifierResponse struct {
	ResponseIndex    int
	MessageIndex     int
	UserMessageCount int
	Content          string
	PrefixDigest     string
}

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

type AtomicClassificationRequest struct {
	User                   AtomicClassifierUser    `json:"user"`
	HistorySource          ClassifierHistorySource `json:"history_source"`
	CompletedResponseCount int                     `json:"completed_response_count"`
}

type AtomicClassifierUser struct {
	CurrentUserMessage string                        `json:"current_user_message"`
	PrecedingResponses []PredictedClassifierResponse `json:"preceding_agent_responses"`
	Features           ClassifierFeatures            `json:"conversation_features"`
}

type PredictedClassifierResponse struct {
	ResponseIndex int                  `json:"response_index"`
	Content       string               `json:"content"`
	Complexity    ClassifierComplexity `json:"complexity"`
}

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
