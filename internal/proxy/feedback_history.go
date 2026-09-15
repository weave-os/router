package proxy

import (
	"context"
	"errors"
	"time"
)

// FeedbackRequest is the compact identity of a successfully completed response.
// Sequence is allocated in completion-commit order within its logical scope.
type FeedbackRequest struct {
	InstallationID  string
	SessionKey      []byte
	Role            string
	RequestID       string
	Sequence        int64
	CompletedAt     time.Time
	ServedModel     string
	ServedProvider  string
	Strategy        string
	RouteID         string
	TrainingAllowed bool
}

// RouterFeedbackStore commits completion history and atomically accepts a command
// with its exact history target and local request rating.
type RouterFeedbackStore interface {
	CompleteFeedbackRequest(context.Context, FeedbackRequest) error
	AcceptRouterFeedback(context.Context, RouterFeedbackEvent) (RouterFeedbackEvent, error)
}

// RouterFeedbackQueue leases accepted commands for recoverable policy reporting.
// Finishing a claim never changes request_feedback or resolves its target again.
type RouterFeedbackQueue interface {
	ClaimRouterFeedback(ctx context.Context, token string, lease time.Duration) (RouterFeedbackEvent, bool, error)
	FinishRouterFeedback(ctx context.Context, id, token, status, lastError string, nextAttempt time.Time) error
	RouterFeedbackTrainingAllowed(ctx context.Context, installationID, externalID string) (bool, error)
}

// ErrFeedbackLeaseLost means the command is now owned by a different worker.
var ErrFeedbackLeaseLost = errors.New("router feedback lease lost")

// RouterFeedbackPending, RouterFeedbackDelivered and RouterFeedbackSkipped are
// the durable policy-delivery states; skipped commands retain their local rating.
const (
	RouterFeedbackPending   = "pending"
	RouterFeedbackDelivered = "delivered"
	RouterFeedbackSkipped   = "skipped"
)
