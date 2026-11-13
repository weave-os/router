package entitlement

import (
	"context"

	"weave-os/router/internal/router/eligibility"
)

// planBoundaries maps each plan to the hard model-eligibility boundary it
// sells. Max is open-source-only: that is what the subscription is, not a
// routing preference, so it holds however the turn ends up funded.
var planBoundaries = map[Plan]eligibility.Boundary{
	PlanMax:   eligibility.MaxOpenSourceOnly,
	PlanBoost: eligibility.Unrestricted(),
}

// deniesEverything is the boundary of a plan with no declared boundary. A plan
// added without one refuses every model rather than serving unrestricted;
// TestPlanBoundariesCoverEveryPlan keeps that from reaching production.
var deniesEverything = eligibility.New("unmapped_plan")

// ModelBoundaryFor returns the hard product boundary of a plan.
func ModelBoundaryFor(plan Plan) eligibility.Boundary {
	boundary, declared := planBoundaries[plan]
	if !declared {
		return deniesEverything
	}
	return boundary
}

type productScopeContextKey struct{}

// WithProductScope stamps the plan whose product boundary governs this
// request. Deliberately separate from WithCoverage: coverage says the included
// allowance is paying for the turn, whereas the boundary applies to every turn
// an individual subscriber makes — including one their spent allowance pushes
// onto prepaid or org capacity.
func WithProductScope(ctx context.Context, plan Plan) context.Context {
	return context.WithValue(ctx, productScopeContextKey{}, plan)
}

// ProductScopeFromContext returns the subscriber's plan, if the request runs
// under an individual subscription.
func ProductScopeFromContext(ctx context.Context) (Plan, bool) {
	plan, ok := ctx.Value(productScopeContextKey{}).(Plan)
	return plan, ok
}

// ModelBoundaryFromContext returns the boundary governing this request, which
// is unrestricted when the caller holds no individual subscription.
func ModelBoundaryFromContext(ctx context.Context) eligibility.Boundary {
	plan, scoped := ProductScopeFromContext(ctx)
	if !scoped {
		return eligibility.Unrestricted()
	}
	return ModelBoundaryFor(plan)
}
