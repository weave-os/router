package subscriptions

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
	"weave-os/router/internal/auth"
)

func TestExcludedPoolAccountCandidateSelection(t *testing.T) {
	accounts := []*auth.SubscriptionAccount{
		{ID: "required-seat", SubscriberID: "owner", Provider: auth.SubscriptionProviderCodex, Enabled: true, State: auth.SubscriptionAccountStateActive},
		{ID: "another-seat", SubscriberID: "owner", Provider: auth.SubscriptionProviderCodex, Enabled: true, State: auth.SubscriptionAccountStateActive},
	}
	eligible, present := eligibleSubscriptionAccounts(context.Background(), auth.SubscriptionOwner{ExcludedAccountIDs: []string{"another-seat"}}, ProviderCodex, accounts, time.Now())
	require.True(t, present)
	require.Len(t, eligible, 1)
	require.Equal(t, "required-seat", eligible[0].ID)
	accounts[0].Enabled = false
	eligible, present = eligibleSubscriptionAccounts(context.Background(), auth.SubscriptionOwner{ExcludedAccountIDs: []string{"another-seat"}}, ProviderCodex, accounts, time.Now())
	require.True(t, present)
	require.Empty(t, eligible)
}
