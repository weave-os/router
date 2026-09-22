package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/server/middleware"
	"weave-os/router/internal/subscriptions/entitlement"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// billingGatedAllowanceChain wires the allowance gate ahead of the balance gate
// exactly as server.go mounts them on the inference routes. The order is load
// bearing and invisible from either middleware on its own: the allowance gate
// marks a covering caller linked-first before any balance is read, so only the
// balance gate running afterwards can upgrade that to credits_depleted.
func billingGatedAllowanceChain(
	t *testing.T,
	repo *stubBillingRepo,
	serve gin.HandlerFunc,
) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	allowanceSvc := entitlement.NewService(
		&stubEntitlements{current: activeSubscriberEntitlement(), found: true},
		&stubAllowances{},
	).WithClock(func() time.Time { return allowanceNow })

	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set("router_api_key", subscriberAPIKey())
		withInstallation(c, "org_precedence")
	})
	engine.Use(middleware.WithSubscriberAllowance(allowanceSvc))
	engine.Use(middleware.WithBalanceCheck(billing.NewService(repo), billing.MinBalanceMicros))
	engine.POST("/v1/messages", serve)
	return engine
}

func TestSubscriptionOnlyReasonPrecedenceAcrossGates(t *testing.T) {
	// A depleted organization whose caller also holds a covering subscription is
	// flagged twice: linked-first by the allowance gate, then credits_depleted by
	// the balance gate. The depleted reason has to be the one that survives, or
	// the turn loses the top-up CTA that says paid fallback is off.
	for name, tc := range map[string]struct {
		balance int64
		want    billing.SubscriptionOnlyReason
	}{
		"funded organization stays linked-first":  {balance: 5_000_000, want: billing.SubscriptionOnlyLinkedFirst},
		"depleted organization reports depletion": {balance: 0, want: billing.SubscriptionOnlyCreditsDepleted},
		"negative balance reports depletion":      {balance: -1_000_000, want: billing.SubscriptionOnlyCreditsDepleted},
	} {
		t.Run(name, func(t *testing.T) {
			var reason billing.SubscriptionOnlyReason
			var flagged bool
			engine := billingGatedAllowanceChain(t, &stubBillingRepo{balance: tc.balance}, func(c *gin.Context) {
				reason, flagged = billing.SubscriptionOnlyReasonFromContext(c.Request.Context())
			})

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			req.Header.Set("Authorization", "Bearer sk-ant-oat-abc123")
			engine.ServeHTTP(w, req)

			assert.Equal(t, http.StatusOK, w.Code, "a covering subscription must never be 402'd")
			require.True(t, flagged)
			assert.Equal(t, tc.want, reason)
		})
	}
}
