package middleware_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/server/middleware"
	"weave-os/router/internal/subscriptions/entitlement"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWithSubscriberAllowance_MaxRejectsLinkedSubscriptionWithoutSpending(t *testing.T) {
	for _, route := range []struct {
		path     string
		provider auth.SubscriptionProvider
		bearer   string
	}{
		{"/v1/messages", auth.SubscriptionProviderClaude, "sk-ant-oat-test"},
		{"/v1/chat/completions", auth.SubscriptionProviderCodex, "eyJhbGciOi.test.signature"},
		{"/v1/responses", auth.SubscriptionProviderCodex, "eyJhbGciOi.test.signature"},
	} {
		for _, enrolled := range []bool{false, true} {
			for _, exhausted := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/enrolled=%t/exhausted=%t", route.path, enrolled, exhausted), func(t *testing.T) {
					gin.SetMode(gin.TestMode)
					allowances := &stubAllowances{}
					if exhausted {
						allowances.billingConsumed = monthlyAllowance
					}
					allowanceSvc := entitlement.NewService(
						&stubEntitlements{current: maxSubscriberEntitlement(), found: true}, allowances,
					).WithClock(func() time.Time { return allowanceNow })
					organizationBilling := &stubBillingRepo{balance: 5_000_000}
					dispatched := false
					engine := gin.New()
					engine.Use(func(c *gin.Context) {
						c.Set("router_api_key", subscriberAPIKey())
						withInstallation(c, "org_subscription_conflict")
					})
					engine.Use(middleware.WithSubscriberAllowance(allowanceSvc))
					engine.Use(middleware.WithBalanceCheck(billing.NewService(organizationBilling), billing.MinBalanceMicros))
					engine.POST(route.path, func(c *gin.Context) {
						dispatched = true
						c.Status(http.StatusOK)
					})

					request := httptest.NewRequest(http.MethodPost, route.path, nil)
					if enrolled {
						request = request.WithContext(context.WithValue(request.Context(), proxy.ManagedSubscriptionProvidersContextKey{}, map[auth.SubscriptionProvider]struct{}{route.provider: {}}))
					} else {
						request.Header.Set("Authorization", "Bearer "+route.bearer)
						if route.provider == auth.SubscriptionProviderCodex {
							request.Header.Set("ChatGPT-Account-ID", "test-account")
						}
					}
					response := httptest.NewRecorder()
					engine.ServeHTTP(response, request)

					require.Equal(t, http.StatusForbidden, response.Code)
					var conflictResponse struct {
						Error   middleware.SubscriptionErrorCode `json:"error"`
						Message string                           `json:"message"`
					}
					require.NoError(t, json.Unmarshal(response.Body.Bytes(), &conflictResponse))
					assert.Equal(t, middleware.SubscriptionPlanConflict, conflictResponse.Error)
					assert.Contains(t, conflictResponse.Message, "Max plan only supports open-source models")
					assert.Contains(t, conflictResponse.Message, "Disable linked-subscription routing")
					assert.Contains(t, conflictResponse.Message, "No included allowance or prepaid credits were used")
					assert.Empty(t, response.Header().Get("Retry-After"))
					assert.False(t, dispatched)
					assert.Empty(t, allowances.held)
					assert.Empty(t, allowances.released)
					assert.Empty(t, organizationBilling.balanceOrgIDs)
				})
			}
		}
	}
}

func TestWithSubscriberAllowance_MaxExplicitSubscriptionOptOutUsesAllowance(t *testing.T) {
	allowances := &stubAllowances{}
	dispatched := false
	engine := gateServing(t, &stubEntitlements{current: maxSubscriberEntitlement(), found: true}, allowances, func(c *gin.Context) {
		coverage, covered := entitlement.CoverageFromContext(c.Request.Context())
		assert.True(t, covered)
		assert.Equal(t, entitlement.PlanMax, coverage.Plan)
		assert.False(t, billing.SubscriptionOnlyFromContext(c.Request.Context()))
		dispatched = true
	})

	request := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	request.Header.Set("Authorization", "Bearer sk-ant-oat-test")
	ctx := context.WithValue(request.Context(), proxy.ManagedSubscriptionProvidersContextKey{}, map[auth.SubscriptionProvider]struct{}{auth.SubscriptionProviderClaude: {}})
	ctx = context.WithValue(ctx, proxy.InstallationSubscriptionRoutingDisabledContextKey{}, true)
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request.WithContext(ctx))

	assert.Equal(t, http.StatusOK, response.Code)
	assert.True(t, dispatched)
	require.Len(t, allowances.held, 1)
	assert.Equal(t, []string{allowances.held[0].ActionID}, allowances.released)
}
