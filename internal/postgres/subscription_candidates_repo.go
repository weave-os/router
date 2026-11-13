package postgres

import (
	"context"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/postgres/dbbudget"
	"weave-os/router/internal/sqlc"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

func (r *subscriptionAccountRepo) ListSubscriptionCandidates(ctx context.Context, owner auth.SubscriptionOwner) ([]*auth.SubscriptionAccount, error) {
	installationID, err := uuid.Parse(owner.InstallationID)
	if err != nil {
		return nil, auth.ErrSubscriptionAccountNotFound
	}
	params := sqlc.ListModelRouterSubscriptionCandidatesParams{InstallationID: installationID}
	switch {
	case owner.SubscriberID != "":
		subscriberID, err := uuid.Parse(owner.SubscriberID)
		if err != nil {
			return nil, auth.ErrSubscriptionAccountNotFound
		}
		params.SubscriberID = pgtype.UUID{Bytes: subscriberID, Valid: true}
	case owner.APIKeyID != "":
		apiKeyID, err := uuid.Parse(owner.APIKeyID)
		if err != nil {
			return nil, auth.ErrSubscriptionAccountNotFound
		}
		params.APIKeyID = pgtype.UUID{Bytes: apiKeyID, Valid: true}
	default:
		return nil, nil
	}
	rows, err := dbbudget.Queries(r.tx).ListModelRouterSubscriptionCandidates(ctx, params)
	if err != nil {
		return nil, err
	}
	accounts := make([]*auth.SubscriptionAccount, 0, len(rows))
	for _, row := range rows {
		account := toAuthSubscriptionAccountFields(row.ID, row.SubscriberID, row.APIKeyID, row.Provider, row.ExternalAccountID, row.ProviderUserID,
			row.DisplayName, row.RefreshTokenCiphertext, row.Enabled, row.HealthState, row.CooldownUntil, row.CreatedAt)
		account.Tier = auth.SubscriptionTier(row.Tier)
		account.AccessTokenMissing = row.AccessTokenMissing
		account.CredentialRejections = int(row.CredentialRejectionCount)
		accounts = append(accounts, account)
	}
	return accounts, nil
}
