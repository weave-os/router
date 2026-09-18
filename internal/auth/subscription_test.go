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

func (r *subscriptionAccountRepoStub) UpsertSubscriptionAccount(_ context.Context, params CreateSubscriptionAccountParams) (*SubscriptionAccount, error) {
	if r.account == nil {
		r.account = &SubscriptionAccount{
			ID: "stable-account-id", APIKeyID: params.APIKeyID, Provider: params.Provider,
			ExternalAccountID: params.ExternalAccountID,
		}
	}
	r.account.RefreshTokenCiphertext = append([]byte(nil), params.RefreshToken...)
	r.account.Enabled = true
	r.account.CooldownUntil = nil
	return r.account, nil
}

func (*subscriptionAccountRepoStub) ListSubscriptionAccounts(context.Context, string) ([]*SubscriptionAccount, error) {
	return nil, nil
}
func (*subscriptionAccountRepoStub) UpdateSubscriptionAccountState(context.Context, string, string, bool, *time.Time) error {
	return nil
}
func (*subscriptionAccountRepoStub) UpdateSubscriptionAccountCooldown(context.Context, string, string, time.Time) error {
	return nil
}
func (*subscriptionAccountRepoStub) UpdateSubscriptionRefreshToken(context.Context, string, string, []byte) error {
	return nil
}
func (*subscriptionAccountRepoStub) DeleteSubscriptionAccount(context.Context, string, string) error {
	return nil
}
func (*subscriptionAccountRepoStub) TryAcquireSubscriptionRefreshLease(context.Context, string, string, string, time.Duration) (RefreshLeaseAcquisition, error) {
	return RefreshLeaseAcquisition{}, nil
}
func (*subscriptionAccountRepoStub) ExtendSubscriptionRefreshLease(context.Context, string, string, string, time.Duration) (int64, error) {
	return 0, nil
}
func (*subscriptionAccountRepoStub) ReleaseSubscriptionRefreshLease(context.Context, string, string, string) error {
	return nil
}
func (*subscriptionAccountRepoStub) GetSubscriptionCredentialRecord(context.Context, string, string) (*SubscriptionCredentialRecord, error) {
	return nil, ErrSubscriptionAccountNotFound
}
func (*subscriptionAccountRepoStub) PersistSubscriptionTokens(context.Context, string, string, string, int64, []byte, []byte, time.Time) error {
	return nil
}

func TestAddSubscriptionAccountUpsertsStableProviderIdentity(t *testing.T) {
	repo := &subscriptionAccountRepoStub{}
	svc := NewService(nil, nil, nil, nil, NoOpAPIKeyCache{}, nil, time.Now).
		WithSubscriptionAccounts(repo)
	params := CreateSubscriptionAccountParams{
		APIKeyID: "owner-1", Provider: SubscriptionProviderCodex,
		ExternalAccountID: "chatgpt-account-1", RefreshToken: []byte("refresh-old"),
	}

	first, err := svc.AddSubscriptionAccount(context.Background(), params)
	require.NoError(t, err)
	params.RefreshToken = []byte("refresh-new")
	second, err := svc.AddSubscriptionAccount(context.Background(), params)
	require.NoError(t, err)

	require.Equal(t, first.ID, second.ID)
	require.Equal(t, []byte("refresh-new"), repo.account.RefreshTokenCiphertext)
	require.True(t, repo.account.Enabled)
	require.Nil(t, repo.account.CooldownUntil)
}

type coordinatedSubscriptionRepo struct {
	*subscriptionAccountRepoStub
	credentialRecord           *SubscriptionCredentialRecord
	persistedRefreshCiphertext []byte
	persistedAccessCiphertext  []byte
}

func (r *coordinatedSubscriptionRepo) TryAcquireSubscriptionRefreshLease(context.Context, string, string, string, time.Duration) (RefreshLeaseAcquisition, error) {
	return RefreshLeaseAcquisition{Acquired: true}, nil
}

func (r *coordinatedSubscriptionRepo) ReleaseSubscriptionRefreshLease(context.Context, string, string, string) error {
	return nil
}

func (r *coordinatedSubscriptionRepo) GetSubscriptionCredentialRecord(context.Context, string, string) (*SubscriptionCredentialRecord, error) {
	return r.credentialRecord, nil
}

func (r *coordinatedSubscriptionRepo) PersistSubscriptionTokens(_ context.Context, _ string, _ string, _ string, _ int64, refreshCiphertext, accessCiphertext []byte, _ time.Time) error {
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

	credentials, err := svc.LoadSubscriptionCredentials(context.Background(), "owner-1", "account-1")
	require.NoError(t, err)
	require.Equal(t, []byte("refresh-secret"), credentials.RefreshToken)
	require.Equal(t, []byte("access-secret"), credentials.AccessToken)
	require.Equal(t, "lease-1", credentials.TokenRefreshLeaseID)
	_, err = enc.Decrypt(accessCiphertext, externalAccountID, string(provider))
	require.Error(t, err)
	require.NoError(t, svc.PersistSubscriptionTokens(context.Background(), "owner-1", "account-1", "lease-1", 0,
		[]byte("refresh-new"), []byte("access-new"), time.Now().Add(time.Hour)))
	refresh, err := enc.Decrypt(repo.persistedRefreshCiphertext, externalAccountID, string(provider))
	require.NoError(t, err)
	access, err := enc.Decrypt(repo.persistedAccessCiphertext, externalAccountID, subscriptionAccessPurpose(provider))
	require.NoError(t, err)
	require.Equal(t, []byte("refresh-new"), refresh)
	require.Equal(t, []byte("access-new"), access)
}

func (*subscriptionAccountRepoStub) DisableSubscriptionAccountIfRefreshHolder(context.Context, string, string, string, int64) error {
	return nil
}
func (*subscriptionAccountRepoStub) CooldownSubscriptionAccountIfRefreshHolder(context.Context, string, string, string, int64, time.Time) error {
	return nil
}
