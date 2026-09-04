package postgres

import (
	"context"
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
		accounts = append(accounts, toAuthSubscriptionAccount(row))
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

func toAuthSubscriptionAccount(row sqlc.RouterModelRouterSubscriptionAccount) *auth.SubscriptionAccount {
	return &auth.SubscriptionAccount{
		ID: row.ID.String(), APIKeyID: row.APIKeyID.String(), Provider: auth.SubscriptionProvider(row.Provider),
		ExternalAccountID: row.ExternalAccountID, RefreshTokenCiphertext: row.RefreshTokenCiphertext,
		Enabled: row.Enabled, CooldownUntil: timestampPtr(row.CooldownUntil), CreatedAt: timestampOrZero(row.CreatedAt),
	}
}
