package postgres

import (
	"context"

	"weave-os/router/internal/conversation"
	"weave-os/router/internal/postgres/dbbudget"
	"weave-os/router/internal/sqlc"

	"github.com/google/uuid"
)

var _ conversation.RouterFeedbackStore = (*TelemetryRepo)(nil)

func (r *TelemetryRepo) InsertRouterFeedback(ctx context.Context, p conversation.RouterFeedbackEvent) error {
	id, err := uuid.Parse(p.InstallationID)
	if err != nil {
		return err
	}
	q := dbbudget.Queries(r.tx)
	return q.InsertRouterFeedback(ctx, sqlc.InsertRouterFeedbackParams{
		InstallationID: id,
		SessionKey:     p.SessionKey,
		Role:           p.Role,
		RouterUserID:   uuidOrNil(p.RouterUserID),
		ClientApp:      stringPtrOrNil(p.ClientApp),
		SessionID:      stringPtrOrNil(p.SessionID),
		RequestedModel: p.RequestedModel,
		ServedModel:    p.ServedModel,
		Rating:         stringPtrOrNil(p.Rating),
		SuggestedLabel: stringPtrOrNil(p.SuggestedLabel),
		Feedback:       p.Feedback,
		Source:         p.Source,
		RequestID:      stringPtrOrNil(p.RequestID),
		RouteID:        stringPtrOrNil(p.RouteID),
	})
}
