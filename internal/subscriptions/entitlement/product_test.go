package entitlement

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/router/eligibility"
)

func TestPlanBoundariesCoverEveryPlan(t *testing.T) {
	for _, plan := range []Plan{PlanMax, PlanBoost} {
		_, declared := planBoundaries[plan]
		assert.Truef(t, declared, "plan %q has no declared product boundary", plan)
	}
}

func TestModelBoundaryForMaxIsOpenSourceOnly(t *testing.T) {
	max := ModelBoundaryFor(PlanMax)
	assert.True(t, max.PermitsSource(eligibility.SourceOpenSource))
	assert.False(t, max.PermitsSource(eligibility.SourceClosedSource))
	assert.False(t, max.PermitsSource(eligibility.SourceUnknown))
}

func TestModelBoundaryForUndeclaredPlanRefusesEverything(t *testing.T) {
	undeclared := ModelBoundaryFor(Plan("enterprise-someday"))
	assert.True(t, undeclared.Restricts())
	assert.False(t, undeclared.PermitsSource(eligibility.SourceOpenSource))
}

func TestModelBoundaryFromContext(t *testing.T) {
	assert.False(t, ModelBoundaryFromContext(context.Background()).Restricts())

	maxCtx := WithProductScope(context.Background(), PlanMax)
	plan, scoped := ProductScopeFromContext(maxCtx)
	assert.True(t, scoped)
	assert.Equal(t, PlanMax, plan)
	assert.False(t, ModelBoundaryFromContext(maxCtx).PermitsSource(eligibility.SourceClosedSource))

	assert.False(t, ModelBoundaryFromContext(WithProductScope(context.Background(), PlanBoost)).Restricts())
}

// The product boundary is independent of who pays: a Max request whose
// included allowance is spent, and which therefore falls through to prepaid or
// organization capacity, keeps the same open-source-only boundary.
func TestProductScopeSurvivesExhaustedAllowance(t *testing.T) {
	spent := Admission{Outcome: AdmissionExhausted, Plan: PlanMax}
	ctx := WithProductScope(context.Background(), spent.Plan)
	boundary := ModelBoundaryFromContext(ctx)
	assert.False(t, boundary.PermitsSource(eligibility.SourceClosedSource))
	assert.True(t, boundary.PermitsSource(eligibility.SourceOpenSource))
}
