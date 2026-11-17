package postgres

import (
	"context"
	"errors"

	"weave-os/router/internal/postgres/dbbudget"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/sqlc"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type FeedbackRepo struct {
	tx sqlc.DBTX
}

func NewFeedbackRepo(tx sqlc.DBTX) *FeedbackRepo {
	return &FeedbackRepo{tx: tx}
}

var _ proxy.FeedbackRepository = (*FeedbackRepo)(nil)

func (r *FeedbackRepo) Upsert(ctx context.Context, p proxy.UpsertFeedbackParams) error {
	installationID, err := uuid.Parse(p.InstallationID)
	if err != nil {
		return err
	}
	source := p.Source
	if source == "" {
		source = "link"
	}
	q := dbbudget.Queries(r.tx)
	return q.UpsertRequestFeedback(ctx, sqlc.UpsertRequestFeedbackParams{
		InstallationID: installationID,
		ExternalID:     p.ExternalID,
		RequestID:      p.RequestID,
		Rating:         p.Rating,
		Comment:        p.Comment,
		Source:         source,
		RouterUserID:   uuidOrNil(p.RouterUserID),
	})
}

func (r *FeedbackRepo) GetContext(ctx context.Context, installationID, requestID string) (proxy.FeedbackContext, error) {
	id, err := uuid.Parse(installationID)
	if err != nil {
		return proxy.FeedbackContext{}, err
	}
	q := dbbudget.Queries(r.tx)

	out := proxy.FeedbackContext{RequestID: requestID}

	tele, err := q.GetRequestForFeedback(ctx, sqlc.GetRequestForFeedbackParams{
		InstallationID: id,
		RequestID:      requestID,
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return proxy.FeedbackContext{}, err
	}
	if err == nil {
		out.ChosenModel = derefString(tele.DecisionModel)
		out.ChosenProvider = derefString(tele.DecisionProvider)
		out.ClientApp = derefString(tele.ClientApp)
		out.RoutedAt = tele.Timestamp.Time
	}

	fb, err := q.GetRequestFeedback(ctx, sqlc.GetRequestFeedbackParams{
		InstallationID: id,
		RequestID:      requestID,
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return proxy.FeedbackContext{}, err
	}
	if err == nil {
		out.Rating = fb.Rating
		out.Comment = fb.Comment
	}
	return out, nil
}
