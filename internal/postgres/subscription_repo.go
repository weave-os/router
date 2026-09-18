package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/sqlc"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

type subscriptionAccountRepo struct{ tx sqlc.DBTX }

// NewSubscriptionAccountRepo constructs the encrypted subscription-account repository.
func NewSubscriptionAccountRepo(tx sqlc.DBTX) auth.SubscriptionAccountRepository {
	return &subscriptionAccountRepo{tx: tx}
}

func (r *subscriptionAccountRepo) UpsertSubscriptionAccount(ctx context.Context, params auth.CreateSubscriptionAccountParams) (*auth.SubscriptionAccount, error) {
	apiKeyID, err := uuid.Parse(params.APIKeyID)
	if err != nil {
		return nil, err
	}
	row, err := sqlc.New(r.tx).UpsertModelRouterSubscriptionAccount(ctx, sqlc.UpsertModelRouterSubscriptionAccountParams{
		APIKeyID: apiKeyID, Provider: string(params.Provider), ExternalAccountID: params.ExternalAccountID,
		RefreshTokenCiphertext: params.RefreshToken,
	})
	if err != nil {
		return nil, err
	}
	return toAuthSubscriptionAccount(row), nil
}

func (r *subscriptionAccountRepo) UpdateSubscriptionAccountCooldown(ctx context.Context, accountID, apiKeyID string, cooldownUntil time.Time) error {
	accountUUID, err := uuid.Parse(accountID)
	if err != nil {
		return err
	}
	keyUUID, err := uuid.Parse(apiKeyID)
	if err != nil {
		return err
	}
	rows, err := sqlc.New(r.tx).UpdateModelRouterSubscriptionAccountCooldown(ctx, sqlc.UpdateModelRouterSubscriptionAccountCooldownParams{
		ID: accountUUID, APIKeyID: keyUUID, CooldownUntil: pgtype.Timestamp{Time: cooldownUntil, Valid: true},
	})
	if err != nil {
		return err
	}
	if rows == 0 {
		return auth.ErrSubscriptionAccountNotFound
	}
	return nil
}

func (r *subscriptionAccountRepo) ListSubscriptionAccounts(ctx context.Context, apiKeyID string) ([]*auth.SubscriptionAccount, error) {
	parsed, err := uuid.Parse(apiKeyID)
	if err != nil {
		return nil, err
	}
	rows, err := sqlc.New(r.tx).ListModelRouterSubscriptionAccounts(ctx, parsed)
	if err != nil {
		return nil, err
	}
	accounts := make([]*auth.SubscriptionAccount, 0, len(rows))
	for _, row := range rows {
		accounts = append(accounts, toAuthSubscriptionAccountListRow(row))
	}
	return accounts, nil
}

func (r *subscriptionAccountRepo) UpdateSubscriptionAccountState(ctx context.Context, accountID, apiKeyID string, enabled bool, cooldownUntil *time.Time) error {
	accountUUID, err := uuid.Parse(accountID)
	if err != nil {
		return err
	}
	keyUUID, err := uuid.Parse(apiKeyID)
	if err != nil {
		return err
	}
	var cooldown pgtype.Timestamp
	if cooldownUntil != nil {
		cooldown = pgtype.Timestamp{Time: *cooldownUntil, Valid: true}
	}
	rows, err := sqlc.New(r.tx).UpdateModelRouterSubscriptionAccountState(ctx, sqlc.UpdateModelRouterSubscriptionAccountStateParams{
		ID: accountUUID, APIKeyID: keyUUID, Enabled: enabled, CooldownUntil: cooldown,
	})
	if err != nil {
		return err
	}
	if rows == 0 {
		return auth.ErrSubscriptionAccountNotFound
	}
	return nil
}

func (r *subscriptionAccountRepo) UpdateSubscriptionRefreshToken(ctx context.Context, accountID, apiKeyID string, ciphertext []byte) error {
	accountUUID, err := uuid.Parse(accountID)
	if err != nil {
		return err
	}
	keyUUID, err := uuid.Parse(apiKeyID)
	if err != nil {
		return err
	}
	rows, err := sqlc.New(r.tx).UpdateModelRouterSubscriptionRefreshToken(ctx, sqlc.UpdateModelRouterSubscriptionRefreshTokenParams{
		ID: accountUUID, APIKeyID: keyUUID, RefreshTokenCiphertext: ciphertext,
	})
	if err != nil {
		return err
	}
	if rows == 0 {
		return auth.ErrSubscriptionAccountNotFound
	}
	return nil
}

func (r *subscriptionAccountRepo) DeleteSubscriptionAccount(ctx context.Context, accountID, apiKeyID string) error {
	accountUUID, err := uuid.Parse(accountID)
	if err != nil {
		return err
	}
	keyUUID, err := uuid.Parse(apiKeyID)
	if err != nil {
		return err
	}
	rows, err := sqlc.New(r.tx).DeleteModelRouterSubscriptionAccount(ctx, sqlc.DeleteModelRouterSubscriptionAccountParams{ID: accountUUID, APIKeyID: keyUUID})
	if err != nil {
		return err
	}
	if rows == 0 {
		return auth.ErrSubscriptionAccountNotFound
	}
	return nil
}

func (r *subscriptionAccountRepo) TryAcquireSubscriptionRefreshLease(ctx context.Context, accountID, apiKeyID, leaseID string, leaseTTL time.Duration) (auth.RefreshLeaseAcquisition, error) {
	accountUUID, err := uuid.Parse(accountID)
	if err != nil {
		return auth.RefreshLeaseAcquisition{}, err
	}
	keyUUID, err := uuid.Parse(apiKeyID)
	if err != nil {
		return auth.RefreshLeaseAcquisition{}, err
	}
	leaseUUID, err := uuid.Parse(leaseID)
	if err != nil {
		return auth.RefreshLeaseAcquisition{}, err
	}
	tookOver, err := sqlc.New(r.tx).TryAcquireModelRouterSubscriptionRefreshLease(ctx, sqlc.TryAcquireModelRouterSubscriptionRefreshLeaseParams{
		ID: accountUUID, APIKeyID: keyUUID, LeaseID: leaseUUID,
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

func (r *subscriptionAccountRepo) ExtendSubscriptionRefreshLease(ctx context.Context, accountID, apiKeyID, leaseID string, leaseTTL time.Duration) (int64, error) {
	accountUUID, err := uuid.Parse(accountID)
	if err != nil {
		return 0, err
	}
	keyUUID, err := uuid.Parse(apiKeyID)
	if err != nil {
		return 0, err
	}
	leaseUUID, err := uuid.Parse(leaseID)
	if err != nil {
		return 0, err
	}
	return sqlc.New(r.tx).ExtendModelRouterSubscriptionRefreshLease(ctx, sqlc.ExtendModelRouterSubscriptionRefreshLeaseParams{
		ID: accountUUID, APIKeyID: keyUUID, LeaseID: leaseUUID,
		LeaseSeconds: int64(leaseTTL / time.Second),
	})
}

func (r *subscriptionAccountRepo) ReleaseSubscriptionRefreshLease(ctx context.Context, accountID, apiKeyID, leaseID string) error {
	accountUUID, err := uuid.Parse(accountID)
	if err != nil {
		return err
	}
	keyUUID, err := uuid.Parse(apiKeyID)
	if err != nil {
		return err
	}
	leaseUUID, err := uuid.Parse(leaseID)
	if err != nil {
		return err
	}
	_, err = sqlc.New(r.tx).ReleaseModelRouterSubscriptionRefreshLease(ctx, sqlc.ReleaseModelRouterSubscriptionRefreshLeaseParams{
		ID: accountUUID, APIKeyID: keyUUID, LeaseID: leaseUUID,
	})
	return err
}

func (r *subscriptionAccountRepo) GetSubscriptionCredentialRecord(ctx context.Context, accountID, apiKeyID string) (*auth.SubscriptionCredentialRecord, error) {
	accountUUID, err := uuid.Parse(accountID)
	if err != nil {
		return nil, err
	}
	keyUUID, err := uuid.Parse(apiKeyID)
	if err != nil {
		return nil, err
	}
	row, err := sqlc.New(r.tx).GetModelRouterSubscriptionCredentialRecord(ctx, sqlc.GetModelRouterSubscriptionCredentialRecordParams{
		ID: accountUUID, APIKeyID: keyUUID,
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
		CooldownUntil:          timestampPtr(row.CooldownUntil),
	}, nil
}

func (r *subscriptionAccountRepo) PersistSubscriptionTokens(ctx context.Context, accountID, apiKeyID, leaseID string, expectedVersion int64, refreshCiphertext, accessCiphertext []byte, accessExpiresAt time.Time) error {
	accountUUID, err := uuid.Parse(accountID)
	if err != nil {
		return err
	}
	keyUUID, err := uuid.Parse(apiKeyID)
	if err != nil {
		return err
	}
	leaseUUID, err := uuid.Parse(leaseID)
	if err != nil {
		return err
	}
	rows, err := sqlc.New(r.tx).PersistModelRouterSubscriptionTokens(ctx, sqlc.PersistModelRouterSubscriptionTokensParams{
		ID: accountUUID, APIKeyID: keyUUID, LeaseID: leaseUUID, ExpectedVersion: expectedVersion,
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
	return toAuthSubscriptionAccountFields(row.ID, row.APIKeyID, row.Provider, row.ExternalAccountID, row.RefreshTokenCiphertext, row.Enabled, row.CooldownUntil, row.CreatedAt)
}

func toAuthSubscriptionAccountListRow(row sqlc.ListModelRouterSubscriptionAccountsRow) *auth.SubscriptionAccount {
	return toAuthSubscriptionAccountFields(row.ID, row.APIKeyID, row.Provider, row.ExternalAccountID, row.RefreshTokenCiphertext, row.Enabled, row.CooldownUntil, row.CreatedAt)
}

func toAuthSubscriptionAccountFields(id, apiKeyID uuid.UUID, provider, externalAccountID string, refreshTokenCiphertext []byte, enabled bool, cooldownUntil, createdAt pgtype.Timestamp) *auth.SubscriptionAccount {
	return &auth.SubscriptionAccount{
		ID: id.String(), APIKeyID: apiKeyID.String(), Provider: auth.SubscriptionProvider(provider),
		ExternalAccountID: externalAccountID, RefreshTokenCiphertext: refreshTokenCiphertext,
		Enabled: enabled, CooldownUntil: timestampPtr(cooldownUntil), CreatedAt: timestampOrZero(createdAt),
	}
}

func (r *subscriptionAccountRepo) DisableSubscriptionAccountIfRefreshHolder(ctx context.Context, accountID, apiKeyID, leaseID string, expectedVersion int64) error {
	accountUUID, err := uuid.Parse(accountID)
	if err != nil {
		return err
	}
	keyUUID, err := uuid.Parse(apiKeyID)
	if err != nil {
		return err
	}
	leaseUUID, err := uuid.Parse(leaseID)
	if err != nil {
		return err
	}
	rows, err := sqlc.New(r.tx).DisableModelRouterSubscriptionAccountIfRefreshHolder(ctx, sqlc.DisableModelRouterSubscriptionAccountIfRefreshHolderParams{
		ID: accountUUID, APIKeyID: keyUUID, LeaseID: leaseUUID, ExpectedVersion: expectedVersion,
	})
	if err != nil {
		return err
	}
	if rows == 0 {
		return auth.ErrSubscriptionRefreshConflict
	}
	return nil
}

func (r *subscriptionAccountRepo) CooldownSubscriptionAccountIfRefreshHolder(ctx context.Context, accountID, apiKeyID, leaseID string, expectedVersion int64, cooldownUntil time.Time) error {
	accountUUID, err := uuid.Parse(accountID)
	if err != nil {
		return err
	}
	keyUUID, err := uuid.Parse(apiKeyID)
	if err != nil {
		return err
	}
	leaseUUID, err := uuid.Parse(leaseID)
	if err != nil {
		return err
	}
	rows, err := sqlc.New(r.tx).CooldownModelRouterSubscriptionAccountIfRefreshHolder(ctx, sqlc.CooldownModelRouterSubscriptionAccountIfRefreshHolderParams{
		ID: accountUUID, APIKeyID: keyUUID, LeaseID: leaseUUID, ExpectedVersion: expectedVersion, CooldownUntil: pgtype.Timestamp{Time: cooldownUntil, Valid: true},
	})
	if err != nil {
		return err
	}
	if rows == 0 {
		return auth.ErrSubscriptionRefreshConflict
	}
	return nil
}
