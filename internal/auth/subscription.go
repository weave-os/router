package auth

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type SubscriptionProvider string

const (
	SubscriptionProviderClaude SubscriptionProvider = "claude"
	SubscriptionProviderCodex  SubscriptionProvider = "codex"
)

type SubscriptionAccountState string

const (
	SubscriptionAccountStateActive            SubscriptionAccountState = "active"
	SubscriptionAccountStateExhausted         SubscriptionAccountState = "exhausted"
	SubscriptionAccountStateCooldown          SubscriptionAccountState = "cooldown"
	SubscriptionAccountStateReconnectRequired SubscriptionAccountState = "reconnect_required"
	SubscriptionAccountStateDisabled          SubscriptionAccountState = "disabled"
	SubscriptionAccountStateUnknown           SubscriptionAccountState = "unknown"
)

func (s SubscriptionAccountState) Routable() bool {
	return s == SubscriptionAccountStateActive || s == SubscriptionAccountStateUnknown
}

type SubscriptionOwner struct {
	SubscriberID       string
	APIKeyID           string
	ExcludedAccountIDs []string
	InstallationID     string
}

type SubscriptionTier string

const (
	SubscriptionTierPersonal SubscriptionTier = "personal"
	SubscriptionTierShared   SubscriptionTier = "shared"
)

func (o SubscriptionOwner) Valid() bool {
	return o.SubscriberID != "" || o.APIKeyID != "" || o.InstallationID != ""
}

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

func SubscriptionOwnerForKey(key *APIKey) SubscriptionOwner {
	if key == nil {
		return SubscriptionOwner{}
	}
	return SubscriptionOwner{SubscriberID: key.CredentialSubjectID, APIKeyID: key.ID, InstallationID: key.InstallationID}
}

type SubscriptionAccount struct {
	Tier                   SubscriptionTier
	ID                     string
	SubscriberID           string
	EnrolledByAPIKeyID     string
	Provider               SubscriptionProvider
	ExternalAccountID      string
	ProviderUserID         string
	DisplayName            string
	RefreshTokenCiphertext []byte
	Enabled                bool
	State                  SubscriptionAccountState
	CooldownUntil          *time.Time
	CreatedAt              time.Time
	AccessTokenMissing     bool
	TokenRefreshVersion    int64
	CredentialRejections   int
}

type CreateSubscriptionAccountParams struct {
	Owner                  SubscriptionOwner
	Provider               SubscriptionProvider
	ExternalAccountID      string
	ProviderUserID         string
	DisplayName            string
	RefreshToken           []byte
	InstallationExternalID string
	VerifiedSignIn         bool
}

type VerifiedCodexEnrollment struct {
	ProviderUserID string
	RefreshToken   []byte
}

type CodexEnrollmentVerifier interface {
	VerifyCodexEnrollment(context.Context, string, []byte) (VerifiedCodexEnrollment, error)
}

type SubscriptionUpsertKind string

const (
	SubscriptionUpsertUpdated    SubscriptionUpsertKind = "updated"
	SubscriptionUpsertInserted   SubscriptionUpsertKind = "inserted"
	SubscriptionUpsertAdopted    SubscriptionUpsertKind = "adopted"
	SubscriptionUpsertReassigned SubscriptionUpsertKind = "reassigned"
)

func (k SubscriptionUpsertKind) FirstConnected() bool {
	return k == SubscriptionUpsertInserted || k == SubscriptionUpsertAdopted || k == SubscriptionUpsertReassigned
}

type SubscriptionAccountRepository interface {
	UpsertSubscriptionAccount(context.Context, CreateSubscriptionAccountParams) (*SubscriptionAccount, SubscriptionUpsertKind, error)
	ListSubscriptionAccounts(context.Context, SubscriptionOwner) ([]*SubscriptionAccount, error)
	UpdateSubscriptionAccountState(context.Context, string, SubscriptionOwner, bool, *time.Time) error
	UpdateSubscriptionAccountCooldown(context.Context, string, SubscriptionOwner, time.Time) error
	UpdateSubscriptionRefreshToken(context.Context, string, SubscriptionOwner, []byte) error
	DeleteSubscriptionAccount(context.Context, string, SubscriptionOwner) error
	SubscriptionRefreshRepository
}

type SubscriptionEnrollmentOwnerResolver interface {
	SubscriberEnrollmentOwner(ctx context.Context, subscriberID string) (SubscriptionOwner, error)
}

var ErrSubscriptionEnrollmentMembershipNotFound = errors.New("subscriber is not an active installation member")

func (s *Service) SubscriberEnrollmentOwner(ctx context.Context, subscriberID string) (SubscriptionOwner, error) {
	resolver, ok := s.subscriptionAccounts.(SubscriptionEnrollmentOwnerResolver)
	if !ok {
		return SubscriptionOwner{}, errors.New("subscription enrollment owner lookup is not configured")
	}
	return resolver.SubscriberEnrollmentOwner(ctx, subscriberID)
}

var ErrSubscriptionAccountNotFound = errors.New("subscription account not found")

var ErrSubscriptionRefreshConflict = errors.New("subscription refresh lost race")

const subscriptionAccessPurposeSuffix = ":access"

func (s *Service) SubscriptionAccountsEnabled() bool {
	return s != nil && s.subscriptionAccounts != nil
}

func (s *Service) AddSubscriptionAccount(ctx context.Context, params CreateSubscriptionAccountParams) (*SubscriptionAccount, error) {
	if s.subscriptionAccounts == nil {
		return nil, errors.New("subscription accounts are not configured")
	}
	if params.Owner.SubscriberID == "" || params.ExternalAccountID == "" || len(params.RefreshToken) == 0 {
		return nil, errors.New("subscription account owner, identity, and refresh token are required")
	}
	if params.Provider != SubscriptionProviderClaude && params.Provider != SubscriptionProviderCodex {
		return nil, errors.New("unsupported subscription provider")
	}
	params.ProviderUserID = ""
	if params.Provider == SubscriptionProviderCodex {
		if s.codexEnrollmentVerifier == nil {
			return nil, errors.New("codex enrollment identity verification is unavailable")
		}
		verified, err := s.codexEnrollmentVerifier.VerifyCodexEnrollment(ctx, params.ExternalAccountID, params.RefreshToken)
		if err != nil {
			return nil, err
		}
		if verified.ProviderUserID == "" || len(verified.RefreshToken) == 0 {
			return nil, errors.New("codex enrollment omitted verified provider user identity")
		}
		params.ProviderUserID = verified.ProviderUserID
		params.RefreshToken = verified.RefreshToken
	}
	ciphertext, err := s.encryptor.Encrypt(params.RefreshToken, params.ExternalAccountID, string(params.Provider))
	if err != nil {
		return nil, err
	}
	account, kind, err := s.subscriptionAccounts.UpsertSubscriptionAccount(ctx, CreateSubscriptionAccountParams{
		Owner: params.Owner, Provider: params.Provider,
		ExternalAccountID: params.ExternalAccountID,
		ProviderUserID:    params.ProviderUserID,
		DisplayName:       normalizeSubscriptionAccountDisplayName(params.DisplayName),
		RefreshToken:      ciphertext,
		VerifiedSignIn:    params.VerifiedSignIn,
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

func (s *Service) UpdateSubscriptionAccountCooldown(ctx context.Context, owner SubscriptionOwner, accountID string, cooldownUntil time.Time) error {
	if s.subscriptionAccounts == nil {
		return errors.New("subscription accounts are not configured")
	}
	return s.subscriptionAccounts.UpdateSubscriptionAccountCooldown(ctx, accountID, owner, cooldownUntil)
}

func (s *Service) ListSubscriptionAccounts(ctx context.Context, owner SubscriptionOwner) ([]*SubscriptionAccount, error) {
	if s.subscriptionAccounts == nil {
		return nil, errors.New("subscription accounts are not configured")
	}
	if !owner.Valid() {
		return nil, nil
	}
	return s.subscriptionAccounts.ListSubscriptionAccounts(ctx, owner)
}

func (s *Service) ListSubscriptionCandidates(ctx context.Context, owner SubscriptionOwner) ([]*SubscriptionAccount, error) {
	repo, ok := s.subscriptionAccounts.(interface {
		ListSubscriptionCandidates(context.Context, SubscriptionOwner) ([]*SubscriptionAccount, error)
	})
	if !ok {
		return nil, errors.New("subscription candidate admission is not configured")
	}
	return repo.ListSubscriptionCandidates(ctx, owner)
}

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

func (s *Service) TryAcquireSubscriptionRefreshLease(ctx context.Context, owner SubscriptionOwner, accountID, leaseID string, leaseTTL time.Duration) (RefreshLeaseAcquisition, error) {
	repo, err := s.subscriptionRefreshRepository()
	if err != nil {
		return RefreshLeaseAcquisition{}, err
	}
	return repo.TryAcquireSubscriptionRefreshLease(ctx, accountID, owner, leaseID, leaseTTL)
}

func (s *Service) ExtendSubscriptionRefreshLease(ctx context.Context, owner SubscriptionOwner, accountID, leaseID string, leaseTTL time.Duration) (bool, error) {
	repo, err := s.subscriptionRefreshRepository()
	if err != nil {
		return false, err
	}
	rows, err := repo.ExtendSubscriptionRefreshLease(ctx, accountID, owner, leaseID, leaseTTL)
	return rows > 0, err
}

func (s *Service) ReleaseSubscriptionRefreshLease(ctx context.Context, owner SubscriptionOwner, accountID, leaseID string) error {
	repo, err := s.subscriptionRefreshRepository()
	if err != nil {
		return err
	}
	return repo.ReleaseSubscriptionRefreshLease(ctx, accountID, owner, leaseID)
}

func (s *Service) DisableSubscriptionAccountIfRefreshHolder(ctx context.Context, owner SubscriptionOwner, accountID, leaseID string, expectedVersion int64) error {
	repo, err := s.subscriptionRefreshRepository()
	if err != nil {
		return err
	}
	return repo.DisableSubscriptionAccountIfRefreshHolder(ctx, accountID, owner, leaseID, expectedVersion)
}

func (s *Service) CooldownSubscriptionAccountIfRefreshHolder(ctx context.Context, owner SubscriptionOwner, accountID, leaseID string, expectedVersion int64, cooldownUntil time.Time) error {
	repo, err := s.subscriptionRefreshRepository()
	if err != nil {
		return err
	}
	return repo.CooldownSubscriptionAccountIfRefreshHolder(ctx, accountID, owner, leaseID, expectedVersion, cooldownUntil)
}

func (s *Service) RecordSubscriptionCredentialRejection(ctx context.Context, owner SubscriptionOwner, accountID string, input SubscriptionCredentialRejectionInput) (SubscriptionCredentialRejection, error) {
	repo, err := s.subscriptionRefreshRepository()
	if err != nil {
		return SubscriptionCredentialRejection{}, err
	}
	return repo.RecordSubscriptionCredentialRejection(ctx, accountID, owner, input)
}

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
		ProviderUserID:      credentialRecord.ProviderUserID,
		RefreshToken:        refreshToken,
		TokenRefreshVersion: credentialRecord.TokenRefreshVersion,
		TokenRefreshedAt:    credentialRecord.TokenRefreshedAt,
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

func (s *Service) UpdateSubscriptionAccountState(ctx context.Context, owner SubscriptionOwner, accountID string, enabled bool, cooldownUntil *time.Time) error {
	if s.subscriptionAccounts == nil {
		return errors.New("subscription accounts are not configured")
	}
	rowsErr := s.subscriptionAccounts.UpdateSubscriptionAccountState(ctx, accountID, owner, enabled, cooldownUntil)
	return rowsErr
}

func (s *Service) UpdateSubscriptionAccountHealth(ctx context.Context, owner SubscriptionOwner, accountID string, state SubscriptionAccountState, enabled bool, cooldownUntil *time.Time) (bool, error) {
	if s.subscriptionAccounts == nil {
		return false, errors.New("subscription accounts are not configured")
	}
	if repository, ok := s.subscriptionAccounts.(interface {
		UpdateSubscriptionAccountHealth(context.Context, string, SubscriptionOwner, SubscriptionAccountState, bool, *time.Time) (bool, error)
	}); ok {
		return repository.UpdateSubscriptionAccountHealth(ctx, accountID, owner, state, enabled, cooldownUntil)
	}
	return false, s.subscriptionAccounts.UpdateSubscriptionAccountState(ctx, accountID, owner, enabled, cooldownUntil)
}

func (s *Service) ReactivateSubscriptionAccountQuota(ctx context.Context, owner SubscriptionOwner, accountID string) (bool, error) {
	repository, ok := s.subscriptionAccounts.(interface {
		ReactivateSubscriptionAccountQuota(context.Context, string, SubscriptionOwner) (bool, error)
	})
	if !ok {
		return false, errors.New("subscription quota reactivation is not configured")
	}
	return repository.ReactivateSubscriptionAccountQuota(ctx, accountID, owner)
}

func (s *Service) DeleteSubscriptionAccount(ctx context.Context, owner SubscriptionOwner, accountID string) error {
	if s.subscriptionAccounts == nil {
		return errors.New("subscription accounts are not configured")
	}
	return s.subscriptionAccounts.DeleteSubscriptionAccount(ctx, accountID, owner)
}
