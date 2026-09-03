package auth

import (
	"context"
	"errors"
	"time"
)

// SubscriptionProvider identifies a provider-specific account pool.
type SubscriptionProvider string

const (
	// SubscriptionProviderClaude is a Claude subscription account.
	SubscriptionProviderClaude SubscriptionProvider = "claude"
	// SubscriptionProviderCodex is a Codex subscription account.
	SubscriptionProviderCodex SubscriptionProvider = "codex"
)

// SubscriptionAccount is the server-side representation of an enrolled
// account. RefreshTokenCiphertext is encrypted storage and must not cross the
// auth/service boundary into an API response.
type SubscriptionAccount struct {
	ID                     string
	APIKeyID               string
	Provider               SubscriptionProvider
	ExternalAccountID      string
	RefreshTokenCiphertext []byte
	Enabled                bool
	CooldownUntil          *time.Time
	CreatedAt              time.Time
}

// CreateSubscriptionAccountParams describes an encrypted account enrollment.
type CreateSubscriptionAccountParams struct {
	APIKeyID          string
	Provider          SubscriptionProvider
	ExternalAccountID string
	RefreshToken      []byte
}

// SubscriptionAccountRepository persists encrypted subscription account state.
type SubscriptionAccountRepository interface {
	UpsertSubscriptionAccount(context.Context, CreateSubscriptionAccountParams) (*SubscriptionAccount, error)
	ListSubscriptionAccounts(context.Context, string) ([]*SubscriptionAccount, error)
	UpdateSubscriptionAccountState(context.Context, string, string, bool, *time.Time) error
	UpdateSubscriptionAccountCooldown(context.Context, string, string, time.Time) error
	UpdateSubscriptionRefreshToken(context.Context, string, string, []byte) error
	DeleteSubscriptionAccount(context.Context, string, string) error
}

// ErrSubscriptionAccountNotFound indicates a state mutation did not match the
// authenticated key owner.
var ErrSubscriptionAccountNotFound = errors.New("subscription account not found")

// SubscriptionAccountsEnabled reports whether this deployment wired the
// optional account repository. Callers use it to leave management routes
// unmounted while the rollout flag is disabled.
func (s *Service) SubscriptionAccountsEnabled() bool {
	return s != nil && s.subscriptionAccounts != nil
}

// AddSubscriptionAccount encrypts and persists a refresh token. The raw token
// is never returned by this method.
func (s *Service) AddSubscriptionAccount(ctx context.Context, params CreateSubscriptionAccountParams) (*SubscriptionAccount, error) {
	if s.subscriptionAccounts == nil {
		return nil, errors.New("subscription accounts are not configured")
	}
	if params.APIKeyID == "" || params.ExternalAccountID == "" || len(params.RefreshToken) == 0 {
		return nil, errors.New("subscription account owner, identity, and refresh token are required")
	}
	if params.Provider != SubscriptionProviderClaude && params.Provider != SubscriptionProviderCodex {
		return nil, errors.New("unsupported subscription provider")
	}
	ciphertext, err := s.encryptor.Encrypt(params.RefreshToken, params.ExternalAccountID, string(params.Provider))
	if err != nil {
		return nil, err
	}
	return s.subscriptionAccounts.UpsertSubscriptionAccount(ctx, CreateSubscriptionAccountParams{
		APIKeyID: params.APIKeyID, Provider: params.Provider,
		ExternalAccountID: params.ExternalAccountID, RefreshToken: ciphertext,
	})
}

// UpdateSubscriptionAccountCooldown records quota state without changing the
// durable enabled flag.
func (s *Service) UpdateSubscriptionAccountCooldown(ctx context.Context, apiKeyID, accountID string, cooldownUntil time.Time) error {
	if s.subscriptionAccounts == nil {
		return errors.New("subscription accounts are not configured")
	}
	return s.subscriptionAccounts.UpdateSubscriptionAccountCooldown(ctx, accountID, apiKeyID, cooldownUntil)
}

// ListSubscriptionAccounts returns account metadata without decrypting tokens.
func (s *Service) ListSubscriptionAccounts(ctx context.Context, apiKeyID string) ([]*SubscriptionAccount, error) {
	if s.subscriptionAccounts == nil {
		return nil, errors.New("subscription accounts are not configured")
	}
	return s.subscriptionAccounts.ListSubscriptionAccounts(ctx, apiKeyID)
}

// SubscriptionRefreshToken decrypts an owner's refresh token for the refresh
// worker. It is intentionally a narrow method and never appears in an API DTO.
func (s *Service) SubscriptionRefreshToken(ctx context.Context, apiKeyID, accountID string) ([]byte, error) {
	accounts, err := s.ListSubscriptionAccounts(ctx, apiKeyID)
	if err != nil {
		return nil, err
	}
	for _, account := range accounts {
		if account.ID == accountID {
			return s.encryptor.Decrypt(account.RefreshTokenCiphertext, account.ExternalAccountID, string(account.Provider))
		}
	}
	return nil, ErrSubscriptionAccountNotFound
}

// UpdateSubscriptionRefreshToken encrypts a rotated refresh token using the
// account's stable identity before replacing the stored ciphertext.
func (s *Service) UpdateSubscriptionRefreshToken(ctx context.Context, apiKeyID, accountID string, refreshToken []byte) error {
	if len(refreshToken) == 0 {
		return errors.New("subscription refresh token is required")
	}
	accounts, err := s.ListSubscriptionAccounts(ctx, apiKeyID)
	if err != nil {
		return err
	}
	for _, account := range accounts {
		if account.ID != accountID {
			continue
		}
		ciphertext, encryptErr := s.encryptor.Encrypt(refreshToken, account.ExternalAccountID, string(account.Provider))
		if encryptErr != nil {
			return encryptErr
		}
		return s.subscriptionAccounts.UpdateSubscriptionRefreshToken(ctx, accountID, apiKeyID, ciphertext)
	}
	return ErrSubscriptionAccountNotFound
}

// UpdateSubscriptionAccountState changes enabled/cooldown state only for the
// authenticated owner's account.
func (s *Service) UpdateSubscriptionAccountState(ctx context.Context, apiKeyID, accountID string, enabled bool, cooldownUntil *time.Time) error {
	if s.subscriptionAccounts == nil {
		return errors.New("subscription accounts are not configured")
	}
	rowsErr := s.subscriptionAccounts.UpdateSubscriptionAccountState(ctx, accountID, apiKeyID, enabled, cooldownUntil)
	return rowsErr
}

// DeleteSubscriptionAccount removes an account only for the authenticated owner.
func (s *Service) DeleteSubscriptionAccount(ctx context.Context, apiKeyID, accountID string) error {
	if s.subscriptionAccounts == nil {
		return errors.New("subscription accounts are not configured")
	}
	return s.subscriptionAccounts.DeleteSubscriptionAccount(ctx, accountID, apiKeyID)
}
