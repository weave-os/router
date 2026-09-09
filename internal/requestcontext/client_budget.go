package requestcontext

import (
	"context"

	"weave-os/router/internal/router"
)

type clientBudgetKey struct{}

// WithClientBudget stores the original inbound evidence before upstream preparation adds capabilities.
func WithClientBudget(ctx context.Context, budget router.ClientBudget) context.Context {
	return context.WithValue(ctx, clientBudgetKey{}, budget)
}

// ClientBudgetFrom returns this request's budget evidence; absence means unknown, not a catalog window.
func ClientBudgetFrom(ctx context.Context) router.ClientBudget {
	budget, _ := ctx.Value(clientBudgetKey{}).(router.ClientBudget)
	return budget
}
