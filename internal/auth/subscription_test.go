package auth

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type subscriptionAccountRepoStub struct {
	account *SubscriptionAccount
}

func (r *subscriptionAccountRepoStub) UpsertSubscriptionAccount(_ context.Context, params CreateSubscriptionAccountParams) (*SubscriptionAccount, SubscriptionUpsertKind, error) {
	kind := SubscriptionUpsertInserted
	if r.account == nil {
		r.account = &SubscriptionAccount{
			ID: "stable-account-id", SubscriberID: params.Owner.SubscriberID,
			EnrolledByAPIKeyID: params.Owner.APIKeyID, Provider: params.Provider,
			ExternalAccountID: params.ExternalAccountID, DisplayName: params.DisplayName,
		}
	} else {
		kind = SubscriptionUpsertUpdated
	}
	r.account.RefreshTokenCiphertext = append([]byte(nil), params.RefreshToken...)
	r.account.Enabled = true
	r.account.CooldownUntil = nil
	return r.account, kind, nil
}

func (*subscriptionAccountRepoStub) ListSubscriptionAccounts(context.Context, SubscriptionOwner) ([]*SubscriptionAccount, error) {
	return nil, nil
}
func (*subscriptionAccountRepoStub) UpdateSubscriptionAccountState(context.Context, string, SubscriptionOwner, bool, *time.Time) error {
	return nil
}
func (*subscriptionAccountRepoStub) UpdateSubscriptionAccountCooldown(context.Context, string, SubscriptionOwner, time.Time) error {
	return nil
}
func (*subscriptionAccountRepoStub) UpdateSubscriptionRefreshToken(context.Context, string, SubscriptionOwner, []byte) error {
	return nil
}
func (*subscriptionAccountRepoStub) DeleteSubscriptionAccount(context.Context, string, SubscriptionOwner) error {
	return nil
}
func (*subscriptionAccountRepoStub) TryAcquireSubscriptionRefreshLease(context.Context, string, SubscriptionOwner, string, time.Duration) (RefreshLeaseAcquisition, error) {
	return RefreshLeaseAcquisition{}, nil
}
func (*subscriptionAccountRepoStub) ExtendSubscriptionRefreshLease(context.Context, string, SubscriptionOwner, string, time.Duration) (int64, error) {
	return 0, nil
}
func (*subscriptionAccountRepoStub) ReleaseSubscriptionRefreshLease(context.Context, string, SubscriptionOwner, string) error {
	return nil
}
func (*subscriptionAccountRepoStub) GetSubscriptionCredentialRecord(context.Context, string, SubscriptionOwner) (*SubscriptionCredentialRecord, error) {
	return nil, ErrSubscriptionAccountNotFound
}
func (*subscriptionAccountRepoStub) PersistSubscriptionTokens(context.Context, string, SubscriptionOwner, string, int64, []byte, []byte, time.Time) error {
	return nil
}

func TestAddSubscriptionAccountUpsertsStableProviderIdentity(t *testing.T) {
	repo := &subscriptionAccountRepoStub{}
	svc := NewService(nil, nil, nil, nil, NoOpAPIKeyCache{}, nil, time.Now).
		WithSubscriptionAccounts(repo)
	params := CreateSubscriptionAccountParams{
		Owner: SubscriptionOwner{SubscriberID: "subscriber-1", APIKeyID: "key-1"}, Provider: SubscriptionProviderCodex,
		ExternalAccountID: "chatgpt-account-1", DisplayName: "Acme: person@example.com", RefreshToken: []byte("refresh-old"),
	}

	first, err := svc.AddSubscriptionAccount(context.Background(), params)
	require.NoError(t, err)
	params.RefreshToken = []byte("refresh-new")
	second, err := svc.AddSubscriptionAccount(context.Background(), params)
	require.NoError(t, err)

	require.Equal(t, first.ID, second.ID)
	require.Equal(t, []byte("refresh-new"), repo.account.RefreshTokenCiphertext)
	require.Equal(t, "Acme: person@example.com", repo.account.DisplayName)
	require.True(t, repo.account.Enabled)
	require.Nil(t, repo.account.CooldownUntil)
}

func TestAddSubscriptionAccountNormalizesDisplayName(t *testing.T) {
	repo := &subscriptionAccountRepoStub{}
	svc := NewService(nil, nil, nil, nil, NoOpAPIKeyCache{}, nil, time.Now).
		WithSubscriptionAccounts(repo)

	_, err := svc.AddSubscriptionAccount(context.Background(), CreateSubscriptionAccountParams{
		Owner: SubscriptionOwner{SubscriberID: "subscriber-1", APIKeyID: "key-1"}, Provider: SubscriptionProviderCodex,
		ExternalAccountID: "chatgpt-account-1", DisplayName: "  Acme:\n\tperson@example.com  ", RefreshToken: []byte("refresh"),
	})
	require.NoError(t, err)
	require.Equal(t, "Acme: person@example.com", repo.account.DisplayName)
}

type coordinatedSubscriptionRepo struct {
	*subscriptionAccountRepoStub
	credentialRecord           *SubscriptionCredentialRecord
	persistedRefreshCiphertext []byte
	persistedAccessCiphertext  []byte
}

func (r *coordinatedSubscriptionRepo) TryAcquireSubscriptionRefreshLease(context.Context, string, SubscriptionOwner, string, time.Duration) (RefreshLeaseAcquisition, error) {
	return RefreshLeaseAcquisition{Acquired: true}, nil
}

func (r *coordinatedSubscriptionRepo) ReleaseSubscriptionRefreshLease(context.Context, string, SubscriptionOwner, string) error {
	return nil
}

func (r *coordinatedSubscriptionRepo) GetSubscriptionCredentialRecord(context.Context, string, SubscriptionOwner) (*SubscriptionCredentialRecord, error) {
	return r.credentialRecord, nil
}

func (r *coordinatedSubscriptionRepo) PersistSubscriptionTokens(_ context.Context, _ string, _ SubscriptionOwner, _ string, _ int64, refreshCiphertext, accessCiphertext []byte, _ time.Time) error {
	r.persistedRefreshCiphertext = append([]byte(nil), refreshCiphertext...)
	r.persistedAccessCiphertext = append([]byte(nil), accessCiphertext...)
	return nil
}

func TestLoadSubscriptionCredentialsUsesPurposeBoundAccessEncryption(t *testing.T) {
	enc := newTestEncryptor(t)
	const externalAccountID = "chatgpt-account-1"
	const provider = SubscriptionProviderCodex
	refreshCiphertext, err := enc.Encrypt([]byte("refresh-secret"), externalAccountID, string(provider))
	require.NoError(t, err)
	accessCiphertext, err := enc.Encrypt([]byte("access-secret"), externalAccountID, subscriptionAccessPurpose(provider))
	require.NoError(t, err)
	repo := &coordinatedSubscriptionRepo{
		subscriptionAccountRepoStub: &subscriptionAccountRepoStub{},
		credentialRecord: &SubscriptionCredentialRecord{
			ExternalAccountID: externalAccountID, Provider: provider,
			RefreshTokenCiphertext: refreshCiphertext, AccessTokenCiphertext: accessCiphertext,
			AccessTokenExpiresAt: func() *time.Time { value := time.Now().Add(time.Hour); return &value }(),
			Enabled:              true,
			TokenRefreshLeaseID:  "lease-1",
		},
	}
	svc := NewService(nil, nil, nil, nil, NoOpAPIKeyCache{}, nil, time.Now).
		WithEncryptor(enc).
		WithSubscriptionAccounts(repo)

	owner := SubscriptionOwner{SubscriberID: "subscriber-1", APIKeyID: "key-1"}
	credentials, err := svc.LoadSubscriptionCredentials(context.Background(), owner, "account-1")
	require.NoError(t, err)
	require.Equal(t, []byte("refresh-secret"), credentials.RefreshToken)
	require.Equal(t, []byte("access-secret"), credentials.AccessToken)
	require.Equal(t, "lease-1", credentials.TokenRefreshLeaseID)
	_, err = enc.Decrypt(accessCiphertext, externalAccountID, string(provider))
	require.Error(t, err)
	require.NoError(t, svc.PersistSubscriptionTokens(context.Background(), owner, "account-1", "lease-1", 0,
		[]byte("refresh-new"), []byte("access-new"), time.Now().Add(time.Hour)))
	refresh, err := enc.Decrypt(repo.persistedRefreshCiphertext, externalAccountID, string(provider))
	require.NoError(t, err)
	access, err := enc.Decrypt(repo.persistedAccessCiphertext, externalAccountID, subscriptionAccessPurpose(provider))
	require.NoError(t, err)
	require.Equal(t, []byte("refresh-new"), refresh)
	require.Equal(t, []byte("access-new"), access)
}

func (*subscriptionAccountRepoStub) DisableSubscriptionAccountIfRefreshHolder(context.Context, string, SubscriptionOwner, string, int64) error {
	return nil
}
func (*subscriptionAccountRepoStub) CooldownSubscriptionAccountIfRefreshHolder(context.Context, string, SubscriptionOwner, string, int64, time.Time) error {
	return nil
}

func TestSubscriptionOwnerForKeyPrefersCredentialSubject(t *testing.T) {
	subscriberOwner := SubscriptionOwnerForKey(&APIKey{ID: "key-1", CredentialSubjectID: "subscriber-1"})
	require.Equal(t, SubscriptionOwner{SubscriberID: "subscriber-1", APIKeyID: "key-1"}, subscriberOwner)
	require.Equal(t, "subscriber:subscriber-1", subscriberOwner.PoolKey())

	// A second harness key of the same subscriber draws from the same pool.
	require.Equal(t, subscriberOwner.PoolKey(),
		SubscriptionOwnerForKey(&APIKey{ID: "key-2", CredentialSubjectID: "subscriber-1"}).PoolKey())

	// A key with no credential subject keeps its own legacy pool.
	legacyOwner := SubscriptionOwnerForKey(&APIKey{ID: "key-3"})
	require.Equal(t, "api_key:key-3", legacyOwner.PoolKey())
	require.True(t, legacyOwner.Valid())
	require.False(t, SubscriptionOwnerForKey(nil).Valid())
	require.Empty(t, SubscriptionOwner{}.PoolKey())

	require.Equal(t, "subscriber:subscriber-1", subscriberOwner.LogKey())
	require.NotContains(t, legacyOwner.LogKey(), "key-3")
	require.Empty(t, SubscriptionOwner{}.LogKey())
}

func TestListSubscriptionAccountsRejectsUnownedCaller(t *testing.T) {
	svc := NewService(nil, nil, nil, nil, NoOpAPIKeyCache{}, nil, time.Now).
		WithSubscriptionAccounts(&subscriptionAccountRepoStub{})
	accounts, err := svc.ListSubscriptionAccounts(context.Background(), SubscriptionOwner{})
	require.NoError(t, err)
	require.Empty(t, accounts)
}
