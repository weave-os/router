package subscriptions

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/auth"
)

func TestApplySubscriptionCredentialsKeepsExhaustedCooldown(t *testing.T) {
	resetAt := time.Date(2026, 9, 21, 18, 0, 0, 0, time.UTC)
	account := applySubscriptionCredentials(Account{
		ID: "account-1", State: auth.SubscriptionAccountStateExhausted, CooldownTil: resetAt,
	}, auth.SubscriptionCredentials{
		AccessToken: []byte("fresh"), State: auth.SubscriptionAccountStateExhausted, CooldownUntil: &resetAt,
	})
	require.Equal(t, resetAt, account.CooldownTil)
	require.Equal(t, auth.SubscriptionAccountStateExhausted, account.State)
	require.Equal(t, "fresh", account.AccessToken)
}
