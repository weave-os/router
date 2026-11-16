package subscriptions

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
	"weave-os/router/internal/auth"
)

type fixedRequiredLeaser struct {
	Leaser
	lease Lease
	calls int
	owner auth.SubscriptionOwner
}

func (l *fixedRequiredLeaser) Lease(_ context.Context, owner auth.SubscriptionOwner, _ Provider, _ string) (Lease, bool, error) {
	l.calls++
	l.owner = owner
	return l.lease, true, nil
}
func TestRequiredLeaseRejectsOtherFunding(t *testing.T) {
	for _, id := range []string{"required-seat", "another-seat"} {
		for _, state := range []auth.SubscriptionAccountState{auth.SubscriptionAccountStateActive, auth.SubscriptionAccountStateExhausted} {
			l := &fixedRequiredLeaser{lease: Lease{AccountID: id, State: state, AccessToken: "synthetic", ProviderAccount: "physical-seat"}}
			_, _, err := LeaseRequired(context.Background(), l, auth.SubscriptionOwner{RequiredAccountID: "required-seat"}, ProviderCodex, "")
			if id == "required-seat" && state == auth.SubscriptionAccountStateActive {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, ErrNoAvailableAccount)
			}
			require.Equal(t, 1, l.calls)
			require.Equal(t, "required-seat", l.owner.RequiredAccountID)
		}
	}
}
func TestRequiredAccountCandidateSelection(t *testing.T) {
	accounts := []*auth.SubscriptionAccount{
		{ID: "required-seat", SubscriberID: "owner", Provider: auth.SubscriptionProviderCodex, Enabled: true, State: auth.SubscriptionAccountStateActive},
		{ID: "another-seat", SubscriberID: "owner", Provider: auth.SubscriptionProviderCodex, Enabled: true, State: auth.SubscriptionAccountStateActive},
	}
	eligible, present := eligibleSubscriptionAccounts(context.Background(), auth.SubscriptionOwner{RequiredAccountID: "required-seat"}, ProviderCodex, accounts, time.Now())
	require.True(t, present)
	require.Len(t, eligible, 1)
	require.Equal(t, "required-seat", eligible[0].ID)
	accounts[0].Enabled = false
	eligible, present = eligibleSubscriptionAccounts(context.Background(), auth.SubscriptionOwner{RequiredAccountID: "required-seat"}, ProviderCodex, accounts, time.Now())
	require.True(t, present)
	require.Empty(t, eligible)
}
