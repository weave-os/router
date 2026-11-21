package feedback

import (
	"context"
	"time"
)

type FeedbackContext struct {
	RequestID      string
	ChosenModel    string
	ChosenProvider string
	ClientApp      string
	RoutedAt       time.Time
	Rating         string
	Comment        *string
}

type UpsertFeedbackParams struct {
	InstallationID string
	ExternalID     string
	RequestID      string
	Rating         string
	Comment        *string
	Source         string
	RouterUserID   string
}

type SubmitFeedbackParams struct {
	InstallationID string
	ExternalID     string
	RequestID      string
	RouterUserID   string
	Rating         string
	Comment        *string
}

type FeedbackRepository interface {
	Upsert(ctx context.Context, p UpsertFeedbackParams) error
	GetContext(ctx context.Context, installationID, requestID string) (FeedbackContext, error)
}
