package postgres

import (
	"context"
	"database/sql"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"time"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/postgres/dbbudget"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/sqlc"
)

func (r *subscriptionAccountRepo) GetSubscriptionCredentialRecord(ctx context.Context, accountID string, owner auth.SubscriptionOwner) (*auth.SubscriptionCredentialRecord, error) {
	accountUUID, err := uuid.Parse(accountID)
	if err != nil {
		return nil, err
	}
	subscriberID, keyID, err := subscriptionOwnerPredicate(owner)
	if err != nil {
		return nil, err
	}
	row, err := dbbudget.Queries(r.tx).GetModelRouterSubscriptionCredentialRecord(ctx, sqlc.GetModelRouterSubscriptionCredentialRecordParams{
		ID: accountUUID, SubscriberID: subscriberID, APIKeyID: keyID,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, auth.ErrSubscriptionAccountNotFound
		}
		return nil, err
	}
	var leaseID string
	if row.TokenRefreshLeaseID.Valid {
		leaseID = uuid.UUID(row.TokenRefreshLeaseID.Bytes).String()
	}
	return &auth.SubscriptionCredentialRecord{
		ExternalAccountID:      row.ExternalAccountID,
		ProviderUserID:         subscriptionProviderUserID(row.ProviderUserID),
		Provider:               auth.SubscriptionProvider(row.Provider),
		RefreshTokenCiphertext: row.RefreshTokenCiphertext,
		AccessTokenCiphertext:  row.AccessTokenCiphertext,
		AccessTokenExpiresAt:   timestampPtr(row.AccessTokenExpiresAt),
		TokenRefreshedAt:       timestamptzPtr(row.TokenRefreshedAt),
		TokenRefreshVersion:    row.TokenRefreshVersion,
		TokenRefreshLeaseID:    leaseID,
		Enabled:                row.Enabled,
		State:                  auth.SubscriptionAccountState(row.HealthState),
		CooldownUntil:          timestampPtr(row.CooldownUntil),
	}, nil
}

func (r *subscriptionAccountRepo) PersistSubscriptionTokens(ctx context.Context, accountID string, owner auth.SubscriptionOwner, leaseID string, expectedVersion int64, refreshCiphertext, accessCiphertext []byte, accessExpiresAt time.Time) error {
	accountUUID, err := uuid.Parse(accountID)
	if err != nil {
		return err
	}
	subscriberID, keyID, err := subscriptionOwnerPredicate(owner)
	if err != nil {
		return err
	}
	leaseUUID, err := uuid.Parse(leaseID)
	if err != nil {
		return err
	}
	rows, err := dbbudget.Queries(r.tx).PersistModelRouterSubscriptionTokens(ctx, sqlc.PersistModelRouterSubscriptionTokensParams{
		ID: accountUUID, SubscriberID: subscriberID, APIKeyID: keyID, LeaseID: leaseUUID, ExpectedVersion: expectedVersion,
		RefreshTokenCiphertext: refreshCiphertext, AccessTokenCiphertext: accessCiphertext,
		AccessTokenExpiresAt: pgtype.Timestamp{Time: accessExpiresAt.UTC(), Valid: true},
	})
	if err != nil {
		return err
	}
	if rows == 0 {
		return auth.ErrSubscriptionRefreshConflict
	}
	return nil
}

func (r *subscriptionAccountRepo) acquireSubscriptionRefreshLease(ctx context.Context, accountID string, owner auth.SubscriptionOwner, leaseID string, leaseTTL time.Duration, maintenance bool) (auth.RefreshLeaseAcquisition, error) {
	accountUUID, err := uuid.Parse(accountID)
	if err != nil {
		return auth.RefreshLeaseAcquisition{}, err
	}
	subscriberID, keyID, err := subscriptionOwnerPredicate(owner)
	if err != nil {
		return auth.RefreshLeaseAcquisition{}, err
	}
	leaseUUID, err := uuid.Parse(leaseID)
	if err != nil {
		return auth.RefreshLeaseAcquisition{}, err
	}
	tookOver, err := dbbudget.Queries(r.tx).TryAcquireModelRouterSubscriptionRefreshLease(ctx, sqlc.TryAcquireModelRouterSubscriptionRefreshLeaseParams{
		ID: accountUUID, SubscriberID: subscriberID, APIKeyID: keyID, LeaseID: leaseUUID,
		LeaseSeconds:       int64(leaseTTL / time.Second),
		AllowClaudeOverage: requestcontext.SubscriptionExtraUsageFrom(ctx),
		AllowQuotaRefresh:  maintenance,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return auth.RefreshLeaseAcquisition{}, nil
		}
		return auth.RefreshLeaseAcquisition{}, err
	}
	return auth.RefreshLeaseAcquisition{Acquired: true, TookOver: tookOver}, nil
}
