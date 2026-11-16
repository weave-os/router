package entitlement

import (
	"context"

	"weave-os/router/internal/router/eligibility"
)

var planBoundaries = map[Plan]eligibility.Boundary{
	PlanMax:   eligibility.MaxOpenSourceOnly,
	PlanBoost: eligibility.Unrestricted(),
}

var deniesEverything = eligibility.New("unmapped_plan")

func ModelBoundaryFor(plan Plan) eligibility.Boundary {
	boundary, declared := planBoundaries[plan]
	if !declared {
		return deniesEverything
	}
	return boundary
}

type productScopeContextKey struct{}

func WithProductScope(ctx context.Context, plan Plan) context.Context {
	return context.WithValue(ctx, productScopeContextKey{}, plan)
}

func ProductScopeFromContext(ctx context.Context) (Plan, bool) {
	plan, ok := ctx.Value(productScopeContextKey{}).(Plan)
	return plan, ok
}

func ModelBoundaryFromContext(ctx context.Context) eligibility.Boundary {
	plan, scoped := ProductScopeFromContext(ctx)
	if !scoped {
		return eligibility.Unrestricted()
	}
	return ModelBoundaryFor(plan)
}
