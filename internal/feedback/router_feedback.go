package feedback

import "context"

type RouterFeedbackStore interface {
	InsertRouterFeedback(ctx context.Context, p RouterFeedbackEvent) error
}

type RouterFeedbackEvent struct {
	InstallationID string
	SessionKey     []byte
	Role           string
	RouterUserID   string
	ClientApp      string
	SessionID      string
	RequestedModel string
	ServedModel    string
	Rating         string
	SuggestedLabel string
	Feedback       string
	Source         string
	RequestID      string
	RouteID        string
}
