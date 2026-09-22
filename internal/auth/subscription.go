package auth

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// SubscriptionProvider identifies a provider-specific account pool.
type SubscriptionProvider string

const (
	// SubscriptionProviderClaude is a Claude subscription account.
	SubscriptionProviderClaude SubscriptionProvider = "claude"
	// SubscriptionProviderCodex is a Codex subscription account.
	SubscriptionProviderCodex SubscriptionProvider = "codex"
)

// SubscriptionAccountState is the credential-free routing health of a linked account.
type SubscriptionAccountState string

const (
	SubscriptionAccountStateActive            SubscriptionAccountState = "active"
	SubscriptionAccountStateExhausted         SubscriptionAccountState = "exhausted"
	SubscriptionAccountStateCooldown          SubscriptionAccountState = "cooldown"
	SubscriptionAccountStateReconnectRequired SubscriptionAccountState = "reconnect_required"
	SubscriptionAccountStateDisabled          SubscriptionAccountState = "disabled"
	SubscriptionAccountStateUnknown           SubscriptionAccountState = "unknown"
)

// Routable reports whether an account may be attempted.
func (s SubscriptionAccountState) Routable() bool {
	return s == SubscriptionAccountStateActive || s == SubscriptionAccountStateUnknown
}

// SubscriptionOwner addresses the linked accounts one authenticated caller may
// serve and manage. SubscriberID is the Router credential subject and survives
// API-key rotation, so it is the runtime pool identity. APIKeyID is enrollment
// attribution, and the only ownership legacy rows have until they are migrated
// or reconnected — a subscriber therefore still reaches the unattributed rows
// of whichever key it is presenting.
type SubscriptionOwner struct {
	SubscriberID string
	APIKeyID     string
}

// Valid reports whether this owner can address any account.
func (o SubscriptionOwner) Valid() bool {
	return o.SubscriberID != "" || o.APIKeyID != ""
}

// PoolKey is the runtime pool identity. Two keys of one subscriber share a
// pool; a key with no subscriber keeps its own legacy pool.
func (o SubscriptionOwner) PoolKey() string {
	switch {
	case o.SubscriberID != "":
		return "subscriber:" + o.SubscriberID
	case o.APIKeyID != "":
		return "api_key:" + o.APIKeyID
	default:
		return ""
	}
}

// LegacyPoolKey is the pool identity of the rows this owner reaches only
// through its api key. It stays separate from PoolKey so a second key of one
// subscriber never serves another key's unattributed accounts, whose
// subscriber is by definition unknown.
func (o SubscriptionOwner) LegacyPoolKey() string {
	if o.APIKeyID == "" {
		return ""
	}
	return "api_key:" + o.APIKeyID
}

// SyncKey identifies the full set of pools this owner reaches, for callers
// caching a pool refresh.
func (o SubscriptionOwner) SyncKey() string {
	return o.PoolKey() + "|" + o.LegacyPoolKey()
}

// LogKey identifies the pool in logs without emitting the api key id: legacy
// pools are distinguished by the account id logged alongside it.
func (o SubscriptionOwner) LogKey() string {
	switch {
	case o.SubscriberID != "":
		return "subscriber:" + o.SubscriberID
	case o.APIKeyID != "":
		return "api_key"
	default:
		return ""
	}
}

// SubscriptionOwnerForKey derives linked-account ownership from an
// authenticated key: its credential subject where one exists, plus the key
// itself for rows enrolled before ownership moved to the subscriber.
func SubscriptionOwnerForKey(key *APIKey) SubscriptionOwner {
	if key == nil {
		return SubscriptionOwner{}
	}
	return SubscriptionOwner{SubscriberID: key.CredentialSubjectID, APIKeyID: key.ID}
}

// SubscriptionAccount is the server-side representation of an enrolled
// account. RefreshTokenCiphertext is encrypted storage and must not cross the
// auth/service boundary into an API response.
type SubscriptionAccount struct {
	ID                 string
	SubscriberID       string
	EnrolledByAPIKeyID string
	Provider           SubscriptionProvider
	ExternalAccountID  string
	// DisplayName is provider-supplied metadata for humans; it is not identity.
	DisplayName            string
	RefreshTokenCiphertext []byte
	Enabled                bool
	State                  SubscriptionAccountState
	CooldownUntil          *time.Time
	CreatedAt              time.Time
}

// CreateSubscriptionAccountParams describes an encrypted account enrollment.
type CreateSubscriptionAccountParams struct {
	Owner             SubscriptionOwner
	Provider          SubscriptionProvider
	ExternalAccountID string
	DisplayName       string
	RefreshToken      []byte
	// InstallationExternalID identifies the authenticated installation for onboarding.
	InstallationExternalID string
}

// SubscriptionUpsertKind reports whether an upsert inserted, adopted a legacy row, or refreshed an existing identity.
type SubscriptionUpsertKind string

const (
	SubscriptionUpsertUpdated  SubscriptionUpsertKind = "updated"
	SubscriptionUpsertInserted SubscriptionUpsertKind = "inserted"
	SubscriptionUpsertAdopted  SubscriptionUpsertKind = "adopted"
)

// FirstConnected reports a genuine first registration, including legacy-row adoption.
func (k SubscriptionUpsertKind) FirstConnected() bool {
	return k == SubscriptionUpsertInserted || k == SubscriptionUpsertAdopted
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
	State                  SubscriptionAccountState
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
	State                SubscriptionAccountState
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
	TryAcquireSubscriptionRefreshLease(context.Context, string, SubscriptionOwner, string, time.Duration) (RefreshLeaseAcquisition, error)
	ExtendSubscriptionRefreshLease(context.Context, string, SubscriptionOwner, string, time.Duration) (int64, error)
	ReleaseSubscriptionRefreshLease(context.Context, string, SubscriptionOwner, string) error
	DisableSubscriptionAccountIfRefreshHolder(context.Context, string, SubscriptionOwner, string, int64) error
	CooldownSubscriptionAccountIfRefreshHolder(context.Context, string, SubscriptionOwner, string, int64, time.Time) error
	GetSubscriptionCredentialRecord(context.Context, string, SubscriptionOwner) (*SubscriptionCredentialRecord, error)
	PersistSubscriptionTokens(context.Context, string, SubscriptionOwner, string, int64, []byte, []byte, time.Time) error
}

// SubscriptionAccountRepository persists encrypted subscription account state
// and coordinates cross-replica refresh leases.
type SubscriptionAccountRepository interface {
	UpsertSubscriptionAccount(context.Context, CreateSubscriptionAccountParams) (*SubscriptionAccount, SubscriptionUpsertKind, error)
	ListSubscriptionAccounts(context.Context, SubscriptionOwner) ([]*SubscriptionAccount, error)
	UpdateSubscriptionAccountState(context.Context, string, SubscriptionOwner, bool, *time.Time) error
	UpdateSubscriptionAccountCooldown(context.Context, string, SubscriptionOwner, time.Time) error
	UpdateSubscriptionRefreshToken(context.Context, string, SubscriptionOwner, []byte) error
	DeleteSubscriptionAccount(context.Context, string, SubscriptionOwner) error
	SubscriptionRefreshRepository
}

// ErrSubscriptionAccountNotFound indicates a state mutation did not match the
// authenticated owner.
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
	if !params.Owner.Valid() || params.ExternalAccountID == "" || len(params.RefreshToken) == 0 {
		return nil, errors.New("subscription account owner, identity, and refresh token are required")
	}
	if params.Provider != SubscriptionProviderClaude && params.Provider != SubscriptionProviderCodex {
		return nil, errors.New("unsupported subscription provider")
	}
	ciphertext, err := s.encryptor.Encrypt(params.RefreshToken, params.ExternalAccountID, string(params.Provider))
	if err != nil {
		return nil, err
	}
	account, kind, err := s.subscriptionAccounts.UpsertSubscriptionAccount(ctx, CreateSubscriptionAccountParams{
		Owner: params.Owner, Provider: params.Provider,
		ExternalAccountID: params.ExternalAccountID,
		DisplayName:       normalizeSubscriptionAccountDisplayName(params.DisplayName),
		RefreshToken:      ciphertext,
	})
	if err != nil {
		return nil, err
	}
	if kind.FirstConnected() && s.onboarding != nil {
		s.onboarding.SubscriptionConnected(SubscriptionConnectedEvent{
			InstallationExternalID: params.InstallationExternalID,
			CredentialSubjectID:    params.Owner.SubscriberID,
			APIKeyID:               params.Owner.APIKeyID,
			AccountID:              account.ID,
			Provider:               account.Provider,
			OccurredAt:             s.now(),
		})
	}
	return account, nil
}

func normalizeSubscriptionAccountDisplayName(value string) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	if utf8.RuneCountInString(value) > 512 {
		value = string([]rune(value)[:512])
	}
	return value
}

// UpdateSubscriptionAccountCooldown records quota state without changing the
// durable enabled flag.
func (s *Service) UpdateSubscriptionAccountCooldown(ctx context.Context, owner SubscriptionOwner, accountID string, cooldownUntil time.Time) error {
	if s.subscriptionAccounts == nil {
		return errors.New("subscription accounts are not configured")
	}
	return s.subscriptionAccounts.UpdateSubscriptionAccountCooldown(ctx, accountID, owner, cooldownUntil)
}

// ListSubscriptionAccounts returns account metadata without decrypting tokens.
func (s *Service) ListSubscriptionAccounts(ctx context.Context, owner SubscriptionOwner) ([]*SubscriptionAccount, error) {
	if s.subscriptionAccounts == nil {
		return nil, errors.New("subscription accounts are not configured")
	}
	if !owner.Valid() {
		return nil, nil
	}
	return s.subscriptionAccounts.ListSubscriptionAccounts(ctx, owner)
}

// SubscriptionRefreshToken decrypts an owner's refresh token for the refresh
// worker. It is intentionally a narrow method and never appears in an API DTO.
func (s *Service) SubscriptionRefreshToken(ctx context.Context, owner SubscriptionOwner, accountID string) ([]byte, error) {
	accounts, err := s.ListSubscriptionAccounts(ctx, owner)
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
func (s *Service) UpdateSubscriptionRefreshToken(ctx context.Context, owner SubscriptionOwner, accountID string, refreshToken []byte) error {
	if len(refreshToken) == 0 {
		return errors.New("subscription refresh token is required")
	}
	accounts, err := s.ListSubscriptionAccounts(ctx, owner)
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
		return s.subscriptionAccounts.UpdateSubscriptionRefreshToken(ctx, accountID, owner, ciphertext)
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
func (s *Service) TryAcquireSubscriptionRefreshLease(ctx context.Context, owner SubscriptionOwner, accountID, leaseID string, leaseTTL time.Duration) (RefreshLeaseAcquisition, error) {
	repo, err := s.subscriptionRefreshRepository()
	if err != nil {
		return RefreshLeaseAcquisition{}, err
	}
	return repo.TryAcquireSubscriptionRefreshLease(ctx, accountID, owner, leaseID, leaseTTL)
}

// ExtendSubscriptionRefreshLease renews a lease this replica still holds while
// its provider refresh is in flight. A false result means the lease was lost.
func (s *Service) ExtendSubscriptionRefreshLease(ctx context.Context, owner SubscriptionOwner, accountID, leaseID string, leaseTTL time.Duration) (bool, error) {
	repo, err := s.subscriptionRefreshRepository()
	if err != nil {
		return false, err
	}
	rows, err := repo.ExtendSubscriptionRefreshLease(ctx, accountID, owner, leaseID, leaseTTL)
	return rows > 0, err
}

// ReleaseSubscriptionRefreshLease releases a lease if this replica still
// owns it. An expired or replaced lease is intentionally left untouched.
func (s *Service) ReleaseSubscriptionRefreshLease(ctx context.Context, owner SubscriptionOwner, accountID, leaseID string) error {
	repo, err := s.subscriptionRefreshRepository()
	if err != nil {
		return err
	}
	return repo.ReleaseSubscriptionRefreshLease(ctx, accountID, owner, leaseID)
}

// DisableSubscriptionAccountIfRefreshHolder rejects failures from stale refreshers.
func (s *Service) DisableSubscriptionAccountIfRefreshHolder(ctx context.Context, owner SubscriptionOwner, accountID, leaseID string, expectedVersion int64) error {
	repo, err := s.subscriptionRefreshRepository()
	if err != nil {
		return err
	}
	return repo.DisableSubscriptionAccountIfRefreshHolder(ctx, accountID, owner, leaseID, expectedVersion)
}

// CooldownSubscriptionAccountIfRefreshHolder rejects failures from stale refreshers.
func (s *Service) CooldownSubscriptionAccountIfRefreshHolder(ctx context.Context, owner SubscriptionOwner, accountID, leaseID string, expectedVersion int64, cooldownUntil time.Time) error {
	repo, err := s.subscriptionRefreshRepository()
	if err != nil {
		return err
	}
	return repo.CooldownSubscriptionAccountIfRefreshHolder(ctx, accountID, owner, leaseID, expectedVersion, cooldownUntil)
}

// LoadSubscriptionCredentials decrypts the current refresh and access tokens
// for the runtime. Access-token decryption happens on cache fill, not per
// inference request.
func (s *Service) LoadSubscriptionCredentials(ctx context.Context, owner SubscriptionOwner, accountID string) (SubscriptionCredentials, error) {
	repo, err := s.subscriptionRefreshRepository()
	if err != nil {
		return SubscriptionCredentials{}, err
	}
	credentialRecord, err := repo.GetSubscriptionCredentialRecord(ctx, accountID, owner)
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
		State:               credentialRecord.State,
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
func (s *Service) PersistSubscriptionTokens(ctx context.Context, owner SubscriptionOwner, accountID, leaseID string, expectedVersion int64, refreshToken, accessToken []byte, expiresAt time.Time) error {
	if len(refreshToken) == 0 || len(accessToken) == 0 {
		return errors.New("subscription refresh result is missing credentials")
	}
	repo, err := s.subscriptionRefreshRepository()
	if err != nil {
		return err
	}
	credentialRecord, err := repo.GetSubscriptionCredentialRecord(ctx, accountID, owner)
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
	return repo.PersistSubscriptionTokens(ctx, accountID, owner, leaseID, expectedVersion, refreshCiphertext, accessCiphertext, expiresAt)
}

func subscriptionAccessPurpose(provider SubscriptionProvider) string {
	return string(provider) + subscriptionAccessPurposeSuffix
}

// UpdateSubscriptionAccountState changes enabled/cooldown state only for the
// authenticated owner's account.
func (s *Service) UpdateSubscriptionAccountState(ctx context.Context, owner SubscriptionOwner, accountID string, enabled bool, cooldownUntil *time.Time) error {
	if s.subscriptionAccounts == nil {
		return errors.New("subscription accounts are not configured")
	}
	rowsErr := s.subscriptionAccounts.UpdateSubscriptionAccountState(ctx, accountID, owner, enabled, cooldownUntil)
	return rowsErr
}

// UpdateSubscriptionAccountHealth records a routing health transition.
func (s *Service) UpdateSubscriptionAccountHealth(ctx context.Context, owner SubscriptionOwner, accountID string, state SubscriptionAccountState, enabled bool, cooldownUntil *time.Time) error {
	if s.subscriptionAccounts == nil {
		return errors.New("subscription accounts are not configured")
	}
	if repository, ok := s.subscriptionAccounts.(interface {
		UpdateSubscriptionAccountHealth(context.Context, string, SubscriptionOwner, SubscriptionAccountState, bool, *time.Time) error
	}); ok {
		return repository.UpdateSubscriptionAccountHealth(ctx, accountID, owner, state, enabled, cooldownUntil)
	}
	return s.subscriptionAccounts.UpdateSubscriptionAccountState(ctx, accountID, owner, enabled, cooldownUntil)
}

// DeleteSubscriptionAccount removes an account only for the authenticated owner.
func (s *Service) DeleteSubscriptionAccount(ctx context.Context, owner SubscriptionOwner, accountID string) error {
	if s.subscriptionAccounts == nil {
		return errors.New("subscription accounts are not configured")
	}
	return s.subscriptionAccounts.DeleteSubscriptionAccount(ctx, accountID, owner)
}
