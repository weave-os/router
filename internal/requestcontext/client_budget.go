package requestcontext

import (
	"context"

	"weave-os/router/internal/router"
)

type clientBudgetKey struct{}

func WithClientBudget(ctx context.Context, budget router.ClientBudget) context.Context {
	return context.WithValue(ctx, clientBudgetKey{}, budget)
}

func ClientBudgetFrom(ctx context.Context) router.ClientBudget {
	budget, _ := ctx.Value(clientBudgetKey{}).(router.ClientBudget)
	return budget
}
