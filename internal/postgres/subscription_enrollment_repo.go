package postgres

import (
	"context"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/postgres/dbbudget"
)

func (r *subscriptionAccountRepo) ListSubscriptionEnrollmentOwners(ctx context.Context) ([]auth.SubscriptionEnrollmentCandidate, error) {
	rows, err := dbbudget.Queries(r.tx).ListSubscriptionEnrollmentOwners(ctx)
	if err != nil {
		return nil, err
	}
	owners := make([]auth.SubscriptionEnrollmentCandidate, 0, len(rows))
	for _, row := range rows {
		owners = append(owners, auth.SubscriptionEnrollmentCandidate{SubscriberID: row.SubscriberID.String(), DisplayName: row.DisplayName, Email: row.Email, KeyPrefix: row.KeyPrefix, KeySuffix: row.KeySuffix})
	}
	return owners, nil
}

func (r *subscriptionAccountRepo) ListSubscriptionSeats(ctx context.Context) ([]auth.SubscriptionSeat, error) {
	rows, err := dbbudget.Queries(r.tx).ListSubscriptionSeatsWithOwners(ctx)
	if err != nil {
		return nil, err
	}
	seats := make([]auth.SubscriptionSeat, 0, len(rows))
	for _, row := range rows {
		seats = append(seats, auth.SubscriptionSeat{
			AccountID:          row.ID.String(),
			SubscriberID:       row.SubscriberID.String(),
			Provider:           auth.SubscriptionProvider(row.Provider),
			DisplayName:        row.DisplayName,
			Enabled:            row.Enabled,
			OwnerDisplayName:   row.OwnerDisplayName,
			OwnerEmail:         row.OwnerEmail,
			OwnerHasRoutingKey: row.OwnerHasRoutingKey,
		})
	}
	return seats, nil
}
