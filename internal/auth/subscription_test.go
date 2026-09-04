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
