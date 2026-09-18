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

// SubscriptionCredentialRecord is the encrypted credential state for one
// enrolled account. It never crosses the auth service boundary in this form.
type SubscriptionCredentialRecord struct {
	ExternalAccountID      string
	Provider               SubscriptionProvider
	RefreshTokenCiphertext []byte
	AccessTokenCiphertext  []byte
	AccessTokenExpiresAt   *time.Time
	TokenRefreshVersion    int64
	TokenRefreshLeaseID    string
	Enabled                bool
	CooldownUntil          *time.Time
}

// SubscriptionCredentials is the decrypted credential state used by the
// subscription runtime. The access token is retained only in process memory
// after this method returns.
type SubscriptionCredentials struct {
	RefreshToken         []byte
	AccessToken          []byte
	AccessTokenExpiresAt *time.Time
	TokenRefreshVersion  int64
	TokenRefreshLeaseID  string
	Enabled              bool
	CooldownUntil        *time.Time
}

// RefreshLeaseAcquisition is the outcome of one refresh-lease attempt.
// TookOver means an expired lease from a holder that never released was
// replaced. That holder may already have spent the refresh token, so the new
// holder must not treat a terminal provider error as proof the account is dead.
type RefreshLeaseAcquisition struct {
	Acquired bool
	TookOver bool
}

// SubscriptionRefreshRepository coordinates refresh leases and encrypted
// credential persistence across router replicas.
type SubscriptionRefreshRepository interface {
	TryAcquireSubscriptionRefreshLease(context.Context, string, string, string, time.Duration) (RefreshLeaseAcquisition, error)
	ExtendSubscriptionRefreshLease(context.Context, string, string, string, time.Duration) (int64, error)
	ReleaseSubscriptionRefreshLease(context.Context, string, string, string) error
	DisableSubscriptionAccountIfRefreshHolder(context.Context, string, string, string, int64) error
	CooldownSubscriptionAccountIfRefreshHolder(context.Context, string, string, string, int64, time.Time) error
	GetSubscriptionCredentialRecord(context.Context, string, string) (*SubscriptionCredentialRecord, error)
	PersistSubscriptionTokens(context.Context, string, string, string, int64, []byte, []byte, time.Time) error
}

// SubscriptionAccountRepository persists encrypted subscription account state
// and coordinates cross-replica refresh leases.
type SubscriptionAccountRepository interface {
	UpsertSubscriptionAccount(context.Context, CreateSubscriptionAccountParams) (*SubscriptionAccount, error)
	ListSubscriptionAccounts(context.Context, string) ([]*SubscriptionAccount, error)
	UpdateSubscriptionAccountState(context.Context, string, string, bool, *time.Time) error
	UpdateSubscriptionAccountCooldown(context.Context, string, string, time.Time) error
	UpdateSubscriptionRefreshToken(context.Context, string, string, []byte) error
	DeleteSubscriptionAccount(context.Context, string, string) error
	SubscriptionRefreshRepository
}

// ErrSubscriptionAccountNotFound indicates a state mutation did not match the
// authenticated key owner.
var ErrSubscriptionAccountNotFound = errors.New("subscription account not found")

// ErrSubscriptionRefreshConflict means the lease or credential version no longer
// permits this refresher to publish tokens or record a failure.
var ErrSubscriptionRefreshConflict = errors.New("subscription refresh lost race")

const subscriptionAccessPurposeSuffix = ":access"

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

func (s *Service) subscriptionRefreshRepository() (SubscriptionRefreshRepository, error) {
	if s.subscriptionAccounts == nil {
		return nil, errors.New("subscription accounts are not configured")
	}
	return s.subscriptionAccounts, nil
}

// TryAcquireSubscriptionRefreshLease reserves an account for one replica's
// provider refresh using the database clock. Acquired=false means it is unavailable.
func (s *Service) TryAcquireSubscriptionRefreshLease(ctx context.Context, apiKeyID, accountID, leaseID string, leaseTTL time.Duration) (RefreshLeaseAcquisition, error) {
	repo, err := s.subscriptionRefreshRepository()
	if err != nil {
		return RefreshLeaseAcquisition{}, err
	}
	return repo.TryAcquireSubscriptionRefreshLease(ctx, accountID, apiKeyID, leaseID, leaseTTL)
}

// ExtendSubscriptionRefreshLease renews a lease this replica still holds while
// its provider refresh is in flight. A false result means the lease was lost.
func (s *Service) ExtendSubscriptionRefreshLease(ctx context.Context, apiKeyID, accountID, leaseID string, leaseTTL time.Duration) (bool, error) {
	repo, err := s.subscriptionRefreshRepository()
	if err != nil {
		return false, err
	}
	rows, err := repo.ExtendSubscriptionRefreshLease(ctx, accountID, apiKeyID, leaseID, leaseTTL)
	return rows > 0, err
}

// ReleaseSubscriptionRefreshLease releases a lease if this replica still
// owns it. An expired or replaced lease is intentionally left untouched.
func (s *Service) ReleaseSubscriptionRefreshLease(ctx context.Context, apiKeyID, accountID, leaseID string) error {
	repo, err := s.subscriptionRefreshRepository()
	if err != nil {
		return err
	}
	return repo.ReleaseSubscriptionRefreshLease(ctx, accountID, apiKeyID, leaseID)
}

// DisableSubscriptionAccountIfRefreshHolder rejects failures from stale refreshers.
func (s *Service) DisableSubscriptionAccountIfRefreshHolder(ctx context.Context, apiKeyID, accountID, leaseID string, expectedVersion int64) error {
	repo, err := s.subscriptionRefreshRepository()
	if err != nil {
		return err
	}
	return repo.DisableSubscriptionAccountIfRefreshHolder(ctx, accountID, apiKeyID, leaseID, expectedVersion)
}

// CooldownSubscriptionAccountIfRefreshHolder rejects failures from stale refreshers.
func (s *Service) CooldownSubscriptionAccountIfRefreshHolder(ctx context.Context, apiKeyID, accountID, leaseID string, expectedVersion int64, cooldownUntil time.Time) error {
	repo, err := s.subscriptionRefreshRepository()
	if err != nil {
		return err
	}
	return repo.CooldownSubscriptionAccountIfRefreshHolder(ctx, accountID, apiKeyID, leaseID, expectedVersion, cooldownUntil)
}

// LoadSubscriptionCredentials decrypts the current refresh and access tokens
// for the runtime. Access-token decryption happens on cache fill, not per
// inference request.
func (s *Service) LoadSubscriptionCredentials(ctx context.Context, apiKeyID, accountID string) (SubscriptionCredentials, error) {
	repo, err := s.subscriptionRefreshRepository()
	if err != nil {
		return SubscriptionCredentials{}, err
	}
	credentialRecord, err := repo.GetSubscriptionCredentialRecord(ctx, accountID, apiKeyID)
	if err != nil {
		return SubscriptionCredentials{}, err
	}
	refreshToken, err := s.encryptor.Decrypt(credentialRecord.RefreshTokenCiphertext, credentialRecord.ExternalAccountID, string(credentialRecord.Provider))
	if err != nil {
		return SubscriptionCredentials{}, err
	}
	credentials := SubscriptionCredentials{
		RefreshToken:        refreshToken,
		TokenRefreshVersion: credentialRecord.TokenRefreshVersion,
		TokenRefreshLeaseID: credentialRecord.TokenRefreshLeaseID,
		Enabled:             credentialRecord.Enabled,
		CooldownUntil:       credentialRecord.CooldownUntil,
	}
	if len(credentialRecord.AccessTokenCiphertext) > 0 {
		accessToken, decryptErr := s.encryptor.Decrypt(credentialRecord.AccessTokenCiphertext, credentialRecord.ExternalAccountID, subscriptionAccessPurpose(credentialRecord.Provider))
		if decryptErr != nil {
			return SubscriptionCredentials{}, decryptErr
		}
		credentials.AccessToken = accessToken
		credentials.AccessTokenExpiresAt = credentialRecord.AccessTokenExpiresAt
	}
	return credentials, nil
}

// PersistSubscriptionTokens encrypts and atomically publishes a provider
// refresh result. The repository rejects stale lease owners or versions.
func (s *Service) PersistSubscriptionTokens(ctx context.Context, apiKeyID, accountID, leaseID string, expectedVersion int64, refreshToken, accessToken []byte, expiresAt time.Time) error {
	if len(refreshToken) == 0 || len(accessToken) == 0 {
		return errors.New("subscription refresh result is missing credentials")
	}
	repo, err := s.subscriptionRefreshRepository()
	if err != nil {
		return err
	}
	credentialRecord, err := repo.GetSubscriptionCredentialRecord(ctx, accountID, apiKeyID)
	if err != nil {
		return err
	}
	refreshCiphertext, err := s.encryptor.Encrypt(refreshToken, credentialRecord.ExternalAccountID, string(credentialRecord.Provider))
	if err != nil {
		return err
	}
	accessCiphertext, err := s.encryptor.Encrypt(accessToken, credentialRecord.ExternalAccountID, subscriptionAccessPurpose(credentialRecord.Provider))
	if err != nil {
		return err
	}
	return repo.PersistSubscriptionTokens(ctx, accountID, apiKeyID, leaseID, expectedVersion, refreshCiphertext, accessCiphertext, expiresAt)
}

func subscriptionAccessPurpose(provider SubscriptionProvider) string {
	return string(provider) + subscriptionAccessPurposeSuffix
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
