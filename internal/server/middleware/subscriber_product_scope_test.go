package middleware_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router/eligibility"
	"weave-os/router/internal/server/middleware"
	"weave-os/router/internal/subscriptions/entitlement"
)

// runProductScopeMiddleware reports the product scope the handler downstream
// of the allowance gate observed.
func runProductScopeMiddleware(
	t *testing.T,
	entitlements *stubEntitlements,
	allowances *stubAllowances,
	authHeader string,
) (bool, context.Context) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	svc := entitlement.NewService(entitlements, allowances).WithClock(func() time.Time { return allowanceNow })

	reached := false
	observed := context.Background()
	engine := gin.New()
	engine.POST("/v1/messages", func(c *gin.Context) {
		c.Set("router_api_key", subscriberAPIKey())
		middleware.WithSubscriberAllowance(svc)(c)
		if c.IsAborted() {
			return
		}
		reached = true
		observed = c.Request.Context()
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	engine.ServeHTTP(httptest.NewRecorder(), req)
	return reached, observed
}

func maxSubscriberEntitlement() entitlement.Entitlement {
	current := activeSubscriberEntitlement()
	current.Plan = entitlement.PlanMax
	return current
}

func TestWithSubscriberAllowance_StampsMaxProductScope(t *testing.T) {
	entitlements := &stubEntitlements{current: maxSubscriberEntitlement(), found: true}

	reached, ctx := runProductScopeMiddleware(t, entitlements, &stubAllowances{billingConsumed: 1_000}, "")

	require.True(t, reached)
	plan, scoped := entitlement.ProductScopeFromContext(ctx)
	require.True(t, scoped)
	assert.Equal(t, entitlement.PlanMax, plan)
	assert.False(t, entitlement.ModelBoundaryFromContext(ctx).PermitsSource(eligibility.SourceClosedSource))
}

// The included allowance is spent, so the turn is no longer funded by the Max
// subscription — and the boundary the subscription sells still applies.
func TestWithSubscriberAllowance_KeepsProductScopeWhenAllowanceIsSpent(t *testing.T) {
	entitlements := &stubEntitlements{current: maxSubscriberEntitlement(), found: true}
	spent := &stubAllowances{billingConsumed: monthlyAllowance}

	reached, ctx := runProductScopeMiddleware(t, entitlements, spent, "Bearer sk-ant-oat01-covering-subscription")

	require.True(t, reached, "a covering subscription still serves a spent allowance")
	plan, scoped := entitlement.ProductScopeFromContext(ctx)
	require.True(t, scoped)
	assert.Equal(t, entitlement.PlanMax, plan)
}

func TestWithSubscriberAllowance_KeepsMaxScopeAfterEntitlementEnds(t *testing.T) {
	ended := maxSubscriberEntitlement()
	ended.Status = entitlement.StatusEnded
	entitlements := &stubEntitlements{current: ended, found: true}

	reached, ctx := runProductScopeMiddleware(t, entitlements, &stubAllowances{}, "")

	require.True(t, reached)
	plan, scoped := entitlement.ProductScopeFromContext(ctx)
	require.True(t, scoped)
	assert.Equal(t, entitlement.PlanMax, plan)
	assert.False(t, entitlement.ModelBoundaryFromContext(ctx).PermitsSource(eligibility.SourceClosedSource))
}

// A caller billed at API pricing (here: an ended Max entitlement) funds a
// covered turn from its own plan too — organization spend is what the linked
// subscription is there to avoid.
func TestWithSubscriberAllowance_EndedMaxServesCoveringSubscriptionFirst(t *testing.T) {
	ended := maxSubscriberEntitlement()
	ended.Status = entitlement.StatusEnded
	entitlements := &stubEntitlements{current: ended, found: true}

	reached, ctx := runProductScopeMiddleware(t, entitlements, &stubAllowances{}, "Bearer sk-ant-oat01-covering-subscription")

	require.True(t, reached)
	assert.True(t, billing.SubscriptionOnlyFromContext(ctx))
}

// Without a credential covering the route there is nothing to hold the turn
// to, so organization-funded PAYG failover stays available.
func TestWithSubscriberAllowance_EndedMaxKeepsOrganizationFallbackWithoutSubscription(t *testing.T) {
	ended := maxSubscriberEntitlement()
	ended.Status = entitlement.StatusEnded
	entitlements := &stubEntitlements{current: ended, found: true}

	reached, ctx := runProductScopeMiddleware(t, entitlements, &stubAllowances{}, "")

	require.True(t, reached)
	assert.False(t, billing.SubscriptionOnlyFromContext(ctx))
}

// An agent-shadow evaluation is Weave's own traffic: it draws no included
// allowance even when the subscriber's is spent, but the plan still bounds
// which model its forced route may dispatch.
func TestWithSubscriberAllowance_ScopesAgentShadowWithoutSpendingAllowance(t *testing.T) {
	gin.SetMode(gin.TestMode)
	entitlements := &stubEntitlements{current: maxSubscriberEntitlement(), found: true}
	spent := &stubAllowances{billingConsumed: monthlyAllowance}
	svc := entitlement.NewService(entitlements, spent).WithClock(func() time.Time { return allowanceNow })

	reached := false
	observed := context.Background()
	engine := gin.New()
	engine.POST("/v1/messages", func(c *gin.Context) {
		c.Set("router_api_key", subscriberAPIKey())
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), proxy.AgentShadowEvalContextKey{}, proxy.AgentShadowEvaluation{
			Model:     "claude-opus-4-8",
			RolloutID: "rollout-1",
			StateID:   "state-1",
		}))
		middleware.WithSubscriberAllowance(svc)(c)
		if c.IsAborted() {
			return
		}
		reached = true
		observed = c.Request.Context()
		c.Status(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer sk-ant-oat01-covering-subscription")
	engine.ServeHTTP(httptest.NewRecorder(), req)

	require.True(t, reached, "a shadow evaluation is not refused by a spent allowance")
	assert.Empty(t, spent.held, "a shadow evaluation holds nothing against the allowance")
	assert.False(t, billing.SubscriptionOnlyFromContext(observed), "shadow traffic must not use a subscriber's linked credential")
	plan, scoped := entitlement.ProductScopeFromContext(observed)
	require.True(t, scoped)
	assert.Equal(t, entitlement.PlanMax, plan)
	assert.False(t, entitlement.ModelBoundaryFromContext(observed).PermitsSource(eligibility.SourceClosedSource))
}

func TestWithSubscriberAllowance_LeavesNonSubscribersUnscoped(t *testing.T) {
	reached, ctx := runProductScopeMiddleware(t, &stubEntitlements{}, &stubAllowances{}, "")

	require.True(t, reached)
	_, scoped := entitlement.ProductScopeFromContext(ctx)
	assert.False(t, scoped)
	assert.False(t, entitlement.ModelBoundaryFromContext(ctx).Restricts())
}
