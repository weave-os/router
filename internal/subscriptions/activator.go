package subscriptions

import (
	"context"

	"weave-os/router/internal/auth"
)

type Activator interface {
	Activate(context.Context, auth.SubscriptionOwner, Provider, string) error
}
