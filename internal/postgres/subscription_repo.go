package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/sqlc"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

const (
	uniqueViolationCode             = "23505"
	subscriberAccountUniqueIndex    = "model_router_subscription_accounts_subscriber_account_idx"
	subscriberEnrollmentMaxAttempts = 3
)

type subscriptionAccountRepo struct{ tx sqlc.DBTX }

// NewSubscriptionAccountRepo constructs the encrypted subscription-account repository.
func NewSubscriptionAccountRepo(tx sqlc.DBTX) auth.SubscriptionAccountRepository {
	return &subscriptionAccountRepo{tx: tx}
}

// subscriptionOwnerPredicate maps an owner onto the two nullable columns every
// ownership predicate reads. An owner that resolves to neither addresses no
// row, so it fails closed instead of matching legacy rows with a NULL owner.
func subscriptionOwnerPredicate(owner auth.SubscriptionOwner) (subscriberID, apiKeyID pgtype.UUID, err error) {
	subscriberID, apiKeyID = uuidOrNil(owner.SubscriberID), uuidOrNil(owner.APIKeyID)
	if !subscriberID.Valid && !apiKeyID.Valid {
		return subscriberID, apiKeyID, auth.ErrSubscriptionAccountNotFound
	}
	return subscriberID, apiKeyID, nil
}

func (r *subscriptionAccountRepo) UpsertSubscriptionAccount(ctx context.Context, params auth.CreateSubscriptionAccountParams) (*auth.SubscriptionAccount, auth.SubscriptionUpsertKind, error) {
	apiKeyID, err := uuid.Parse(params.Owner.APIKeyID)
	if err != nil {
		return nil, auth.SubscriptionUpsertUpdated, err
	}
	if params.Owner.SubscriberID == "" {
		row, legacyErr := sqlc.New(r.tx).UpsertModelRouterSubscriptionAccount(ctx, sqlc.UpsertModelRouterSubscriptionAccountParams{
			APIKeyID: apiKeyID, Provider: string(params.Provider), ExternalAccountID: params.ExternalAccountID,
			DisplayName: optionalSubscriptionAccountDisplayName(params.DisplayName), RefreshTokenCiphertext: params.RefreshToken,
		})
		if legacyErr != nil {
			return nil, auth.SubscriptionUpsertUpdated, legacyErr
		}
		return toAuthSubscriptionAccountFields(row.ID, row.SubscriberID, row.APIKeyID, row.Provider, row.ExternalAccountID,
			row.DisplayName, row.RefreshTokenCiphertext, row.Enabled, row.HealthState, row.CooldownUntil, row.CreatedAt), subscriptionUpsertKind(row.Inserted, row.Adopted), nil
	}
	subscriberID, err := uuid.Parse(params.Owner.SubscriberID)
	if err != nil {
		return nil, auth.SubscriptionUpsertUpdated, err
	}
	// Concurrent enrollments can each adopt a different legacy duplicate of the
	// same account, so the loser hits the subscriber-owned unique index. A retry
	// reads the winning row and adopts that one instead of a second duplicate.
	for attempt := 0; ; attempt++ {
		row, err := sqlc.New(r.tx).UpsertModelRouterSubscriptionAccountForSubscriber(ctx, sqlc.UpsertModelRouterSubscriptionAccountForSubscriberParams{
			SubscriberID: subscriberID, APIKeyID: apiKeyID, Provider: string(params.Provider),
			ExternalAccountID: params.ExternalAccountID, DisplayName: optionalSubscriptionAccountDisplayName(params.DisplayName), RefreshTokenCiphertext: params.RefreshToken,
		})
		if err == nil {
			return toAuthSubscriptionAccountFields(row.ID, row.SubscriberID, row.APIKeyID, row.Provider, row.ExternalAccountID,
				row.DisplayName, row.RefreshTokenCiphertext, row.Enabled, row.HealthState, row.CooldownUntil, row.CreatedAt), subscriptionUpsertKind(row.Inserted, row.Adopted), nil
		}
		if attempt == subscriberEnrollmentMaxAttempts-1 || !isSubscriberAccountConflict(err) {
			return nil, auth.SubscriptionUpsertUpdated, err
		}
	}
}

func isSubscriberAccountConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) &&
		pgErr.Code == uniqueViolationCode &&
		pgErr.ConstraintName == subscriberAccountUniqueIndex
}

func (r *subscriptionAccountRepo) UpdateSubscriptionAccountCooldown(ctx context.Context, accountID string, owner auth.SubscriptionOwner, cooldownUntil time.Time) error {
	accountUUID, err := uuid.Parse(accountID)
	if err != nil {
		return err
	}
	subscriberID, keyID, err := subscriptionOwnerPredicate(owner)
	if err != nil {
		return err
	}
	rows, err := sqlc.New(r.tx).UpdateModelRouterSubscriptionAccountCooldown(ctx, sqlc.UpdateModelRouterSubscriptionAccountCooldownParams{
		ID: accountUUID, SubscriberID: subscriberID, APIKeyID: keyID,
		CooldownUntil: pgtype.Timestamp{Time: cooldownUntil, Valid: true},
	})
	if err != nil {
		return err
	}
	if rows == 0 {
		return auth.ErrSubscriptionAccountNotFound
	}
	return nil
}

func (r *subscriptionAccountRepo) ListSubscriptionAccounts(ctx context.Context, owner auth.SubscriptionOwner) ([]*auth.SubscriptionAccount, error) {
	subscriberID, keyID, err := subscriptionOwnerPredicate(owner)
	if err != nil {
		return nil, err
	}
	rows, err := sqlc.New(r.tx).ListModelRouterSubscriptionAccounts(ctx, sqlc.ListModelRouterSubscriptionAccountsParams{
		SubscriberID: subscriberID, APIKeyID: keyID,
	})
	if err != nil {
		return nil, err
	}
	accounts := make([]*auth.SubscriptionAccount, 0, len(rows))
	for _, row := range rows {
		accounts = append(accounts, toAuthSubscriptionAccountListRow(row))
	}
	return accounts, nil
}

func (r *subscriptionAccountRepo) UpdateSubscriptionAccountState(ctx context.Context, accountID string, owner auth.SubscriptionOwner, enabled bool, cooldownUntil *time.Time) error {
	accountUUID, err := uuid.Parse(accountID)
	if err != nil {
		return err
	}
	subscriberID, keyID, err := subscriptionOwnerPredicate(owner)
	if err != nil {
		return err
	}
	var cooldown pgtype.Timestamp
	if cooldownUntil != nil {
		cooldown = pgtype.Timestamp{Time: *cooldownUntil, Valid: true}
	}
	rows, err := sqlc.New(r.tx).UpdateModelRouterSubscriptionAccountState(ctx, sqlc.UpdateModelRouterSubscriptionAccountStateParams{
		ID: accountUUID, SubscriberID: subscriberID, APIKeyID: keyID, Enabled: enabled, CooldownUntil: cooldown,
	})
	if err != nil {
		return err
	}
	if rows == 0 {
		return auth.ErrSubscriptionAccountNotFound
	}
	return nil
}

func (r *subscriptionAccountRepo) UpdateSubscriptionAccountHealth(ctx context.Context, accountID string, owner auth.SubscriptionOwner, state auth.SubscriptionAccountState, enabled bool, cooldownUntil *time.Time) error {
	accountUUID, err := uuid.Parse(accountID)
	if err != nil {
		return err
	}
	subscriberID, keyID, err := subscriptionOwnerPredicate(owner)
	if err != nil {
		return err
	}
	var cooldown pgtype.Timestamp
	if cooldownUntil != nil {
		cooldown = pgtype.Timestamp{Time: *cooldownUntil, Valid: true}
	}
	rows, err := sqlc.New(r.tx).UpdateModelRouterSubscriptionAccountHealth(ctx, sqlc.UpdateModelRouterSubscriptionAccountHealthParams{
		ID: accountUUID, SubscriberID: subscriberID, APIKeyID: keyID,
		HealthState: string(state), Enabled: enabled, CooldownUntil: cooldown,
	})
	if err != nil {
		return err
	}
	if rows == 0 {
		return auth.ErrSubscriptionAccountNotFound
	}
	return nil
}

func (r *subscriptionAccountRepo) UpdateSubscriptionRefreshToken(ctx context.Context, accountID string, owner auth.SubscriptionOwner, ciphertext []byte) error {
	accountUUID, err := uuid.Parse(accountID)
	if err != nil {
		return err
	}
	subscriberID, keyID, err := subscriptionOwnerPredicate(owner)
	if err != nil {
		return err
	}
	rows, err := sqlc.New(r.tx).UpdateModelRouterSubscriptionRefreshToken(ctx, sqlc.UpdateModelRouterSubscriptionRefreshTokenParams{
		ID: accountUUID, SubscriberID: subscriberID, APIKeyID: keyID, RefreshTokenCiphertext: ciphertext,
	})
	if err != nil {
		return err
	}
	if rows == 0 {
		return auth.ErrSubscriptionAccountNotFound
	}
	return nil
}

func (r *subscriptionAccountRepo) DeleteSubscriptionAccount(ctx context.Context, accountID string, owner auth.SubscriptionOwner) error {
	accountUUID, err := uuid.Parse(accountID)
	if err != nil {
		return err
	}
	subscriberID, keyID, err := subscriptionOwnerPredicate(owner)
	if err != nil {
		return err
	}
	rows, err := sqlc.New(r.tx).DeleteModelRouterSubscriptionAccount(ctx, sqlc.DeleteModelRouterSubscriptionAccountParams{
		ID: accountUUID, SubscriberID: subscriberID, APIKeyID: keyID,
	})
	if err != nil {
		return err
	}
	if rows == 0 {
		return auth.ErrSubscriptionAccountNotFound
	}
	return nil
}

func (r *subscriptionAccountRepo) TryAcquireSubscriptionRefreshLease(ctx context.Context, accountID string, owner auth.SubscriptionOwner, leaseID string, leaseTTL time.Duration) (auth.RefreshLeaseAcquisition, error) {
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
	tookOver, err := sqlc.New(r.tx).TryAcquireModelRouterSubscriptionRefreshLease(ctx, sqlc.TryAcquireModelRouterSubscriptionRefreshLeaseParams{
		ID: accountUUID, SubscriberID: subscriberID, APIKeyID: keyID, LeaseID: leaseUUID,
		LeaseSeconds: int64(leaseTTL / time.Second),
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return auth.RefreshLeaseAcquisition{}, nil
		}
		return auth.RefreshLeaseAcquisition{}, err
	}
	return auth.RefreshLeaseAcquisition{Acquired: true, TookOver: tookOver}, nil
}

func (r *subscriptionAccountRepo) ExtendSubscriptionRefreshLease(ctx context.Context, accountID string, owner auth.SubscriptionOwner, leaseID string, leaseTTL time.Duration) (int64, error) {
	accountUUID, err := uuid.Parse(accountID)
	if err != nil {
		return 0, err
	}
	subscriberID, keyID, err := subscriptionOwnerPredicate(owner)
	if err != nil {
		return 0, err
	}
	leaseUUID, err := uuid.Parse(leaseID)
	if err != nil {
		return 0, err
	}
	return sqlc.New(r.tx).ExtendModelRouterSubscriptionRefreshLease(ctx, sqlc.ExtendModelRouterSubscriptionRefreshLeaseParams{
		ID: accountUUID, SubscriberID: subscriberID, APIKeyID: keyID, LeaseID: leaseUUID,
		LeaseSeconds: int64(leaseTTL / time.Second),
	})
}

func (r *subscriptionAccountRepo) ReleaseSubscriptionRefreshLease(ctx context.Context, accountID string, owner auth.SubscriptionOwner, leaseID string) error {
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
	_, err = sqlc.New(r.tx).ReleaseModelRouterSubscriptionRefreshLease(ctx, sqlc.ReleaseModelRouterSubscriptionRefreshLeaseParams{
		ID: accountUUID, SubscriberID: subscriberID, APIKeyID: keyID, LeaseID: leaseUUID,
	})
	return err
}

func (r *subscriptionAccountRepo) GetSubscriptionCredentialRecord(ctx context.Context, accountID string, owner auth.SubscriptionOwner) (*auth.SubscriptionCredentialRecord, error) {
	accountUUID, err := uuid.Parse(accountID)
	if err != nil {
		return nil, err
	}
	subscriberID, keyID, err := subscriptionOwnerPredicate(owner)
	if err != nil {
		return nil, err
	}
	row, err := sqlc.New(r.tx).GetModelRouterSubscriptionCredentialRecord(ctx, sqlc.GetModelRouterSubscriptionCredentialRecordParams{
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
		Provider:               auth.SubscriptionProvider(row.Provider),
		RefreshTokenCiphertext: row.RefreshTokenCiphertext,
		AccessTokenCiphertext:  row.AccessTokenCiphertext,
		AccessTokenExpiresAt:   timestampPtr(row.AccessTokenExpiresAt),
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
	rows, err := sqlc.New(r.tx).PersistModelRouterSubscriptionTokens(ctx, sqlc.PersistModelRouterSubscriptionTokensParams{
		ID: accountUUID, SubscriberID: subscriberID, APIKeyID: keyID, LeaseID: leaseUUID, ExpectedVersion: expectedVersion,
		RefreshTokenCiphertext: refreshCiphertext, AccessTokenCiphertext: accessCiphertext,
		AccessTokenExpiresAt: pgtype.Timestamp{Time: accessExpiresAt, Valid: true},
	})
	if err != nil {
		return err
	}
	if rows == 0 {
		return auth.ErrSubscriptionRefreshConflict
	}
	return nil
}

func toAuthSubscriptionAccount(row sqlc.RouterModelRouterSubscriptionAccount) *auth.SubscriptionAccount {
	return toAuthSubscriptionAccountFields(row.ID, row.SubscriberID, row.APIKeyID, row.Provider, row.ExternalAccountID, row.DisplayName, row.RefreshTokenCiphertext, row.Enabled, row.HealthState, row.CooldownUntil, row.CreatedAt)
}

func toAuthSubscriptionAccountListRow(row sqlc.ListModelRouterSubscriptionAccountsRow) *auth.SubscriptionAccount {
	return toAuthSubscriptionAccountFields(row.ID, row.SubscriberID, row.APIKeyID, row.Provider, row.ExternalAccountID, row.DisplayName, row.RefreshTokenCiphertext, row.Enabled, row.HealthState, row.CooldownUntil, row.CreatedAt)
}

func toAuthSubscriptionAccountFields(id uuid.UUID, subscriberID, apiKeyID pgtype.UUID, provider, externalAccountID string, displayName *string, refreshTokenCiphertext []byte, enabled bool, healthState string, cooldownUntil, createdAt pgtype.Timestamp) *auth.SubscriptionAccount {
	var label string
	if displayName != nil {
		label = *displayName
	}
	return &auth.SubscriptionAccount{
		ID: id.String(), SubscriberID: uuidString(subscriberID), EnrolledByAPIKeyID: uuidString(apiKeyID),
		Provider:          auth.SubscriptionProvider(provider),
		ExternalAccountID: externalAccountID, DisplayName: label, RefreshTokenCiphertext: refreshTokenCiphertext,
		Enabled: enabled, State: auth.SubscriptionAccountState(healthState),
		CooldownUntil: timestampPtr(cooldownUntil), CreatedAt: timestampOrZero(createdAt),
	}
}

func optionalSubscriptionAccountDisplayName(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func (r *subscriptionAccountRepo) DisableSubscriptionAccountIfRefreshHolder(ctx context.Context, accountID string, owner auth.SubscriptionOwner, leaseID string, expectedVersion int64) error {
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
	rows, err := sqlc.New(r.tx).DisableModelRouterSubscriptionAccountIfRefreshHolder(ctx, sqlc.DisableModelRouterSubscriptionAccountIfRefreshHolderParams{
		ID: accountUUID, SubscriberID: subscriberID, APIKeyID: keyID, LeaseID: leaseUUID, ExpectedVersion: expectedVersion,
	})
	if err != nil {
		return err
	}
	if rows == 0 {
		return auth.ErrSubscriptionRefreshConflict
	}
	return nil
}

func (r *subscriptionAccountRepo) CooldownSubscriptionAccountIfRefreshHolder(ctx context.Context, accountID string, owner auth.SubscriptionOwner, leaseID string, expectedVersion int64, cooldownUntil time.Time) error {
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
	rows, err := sqlc.New(r.tx).CooldownModelRouterSubscriptionAccountIfRefreshHolder(ctx, sqlc.CooldownModelRouterSubscriptionAccountIfRefreshHolderParams{
		ID: accountUUID, SubscriberID: subscriberID, APIKeyID: keyID, LeaseID: leaseUUID, ExpectedVersion: expectedVersion,
		CooldownUntil: pgtype.Timestamp{Time: cooldownUntil, Valid: true},
	})
	if err != nil {
		return err
	}
	if rows == 0 {
		return auth.ErrSubscriptionRefreshConflict
	}
	return nil
}

func subscriptionUpsertKind(inserted, adopted bool) auth.SubscriptionUpsertKind {
	switch {
	case inserted:
		return auth.SubscriptionUpsertInserted
	case adopted:
		return auth.SubscriptionUpsertAdopted
	default:
		return auth.SubscriptionUpsertUpdated
	}
}
