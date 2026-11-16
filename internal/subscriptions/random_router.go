package subscriptions

import (
	"slices"
	"weave-os/router/internal/auth"
)

func subscriptionAccountDenied(owner auth.SubscriptionOwner, account string) bool {
	return owner.RequiredAccountID != "" && owner.RequiredAccountID != account || slices.Contains(owner.ExcludedAccountIDs, account)
}
