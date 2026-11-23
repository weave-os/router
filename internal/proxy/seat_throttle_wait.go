package proxy

import (
	"context"

	"weave-os/router/internal/credentials"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
)

func anotherSeatPoolCanServe(ctx context.Context, admission seatAdmission, models []string, target router.Decision) bool {
	pool, _ := credentials.ManagedSubscriptionProviderFromUpstream(target.Provider, target.Model)
	for _, model := range models {
		entry, _ := catalog.ByID(model)
		for _, binding := range entry.Providers {
			other, eligible := credentials.ManagedSubscriptionProviderFromUpstream(binding.Provider, model)
			if eligible && other != pool && admission.mayAdmit(ctx, binding.Provider, model) {
				return true
			}
		}
	}
	return false
}
