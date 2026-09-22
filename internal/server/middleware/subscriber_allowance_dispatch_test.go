package middleware_test

import (
	"context"
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

// gateServing registers the gate as middleware and serves the route with the
// given handler, so the handler runs inside the gate the way a dispatched turn
// does rather than after it.
func gateServing(
	t *testing.T,
	entitlements *stubEntitlements,
	allowances *stubAllowances,
	serve gin.HandlerFunc,
) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	svc := entitlement.NewService(entitlements, allowances).WithClock(func() time.Time { return allowanceNow })

	engine := gin.New()
	engine.Use(func(c *gin.Context) { c.Set("router_api_key", subscriberAPIKey()) })
	engine.Use(middleware.WithSubscriberAllowance(svc))
	engine.POST("/v1/messages", serve)
	return engine
}

func TestWithSubscriberAllowance_ReleasesHoldAfterTheClientHangsUp(t *testing.T) {
	// A client that disconnects mid-turn cancels the request context, and a
	// release issued under it fails — leaving the bound drawn against the
	// subscriber's window until the window turns over, so every abandoned turn
	// costs them allowance no work was served on.
	entitlements := &stubEntitlements{current: activeSubscriberEntitlement(), found: true}
	allowances := &stubAllowances{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	engine := gateServing(t, entitlements, allowances, func(c *gin.Context) { cancel() })

	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(ctx))

	require.Len(t, allowances.held, 1)
	assert.Equal(t, []string{allowances.held[0].ActionID}, allowances.released)
	assert.NoError(t, allowances.releaseCtxErr, "the release must outlive the request whose bound it returns")
}

func TestWithSubscriberAllowance_SpentAllowanceServesSubscriptionOnly(t *testing.T) {
	// The turn continues on the caller's own plan, so routing has to be held to
	// it the way the balance and spend-cap gates hold theirs: an unmarked turn
	// is free to fall back onto paid capacity, which is the spend a spent
	// allowance refuses.
	for name, allowances := range map[string]*stubAllowances{
		"window read as spent": {billingConsumed: monthlyAllowance},
		"reservation refused":  {exhausted: entitlement.PeriodKindSixHour},
	} {
		t.Run(name, func(t *testing.T) {
			entitlements := &stubEntitlements{current: activeSubscriberEntitlement(), found: true}
			subscriptionOnly := false
			engine := gateServing(t, entitlements, allowances, func(c *gin.Context) {
				subscriptionOnly = billing.SubscriptionOnlyFromContext(c.Request.Context())
			})

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			req.Header.Set("Authorization", "Bearer sk-ant-oat-abc123")
			engine.ServeHTTP(w, req)

			assert.Equal(t, http.StatusOK, w.Code)
			assert.True(t, subscriptionOnly)
		})
	}
}

func TestWithSubscriberAllowance_CoveringSubscriptionIsLinkedFirstNotDepleted(t *testing.T) {
	// Linked-first funding marks the turn subscription-only on every plan,
	// including one whose allowance is untouched. Reusing the depleted-credits
	// reason here told healthy, fully-funded organizations their credits were
	// gone on every turn, because the proxy picks the caller-facing marker from
	// the reason alone.
	entitlements := &stubEntitlements{current: activeSubscriberEntitlement(), found: true}
	allowances := &stubAllowances{}

	var reason billing.SubscriptionOnlyReason
	var flagged bool
	engine := gateServing(t, entitlements, allowances, func(c *gin.Context) {
		reason, flagged = billing.SubscriptionOnlyReasonFromContext(c.Request.Context())
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer sk-ant-oat-abc123")
	engine.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	require.True(t, flagged, "a covering subscription must still pin the turn to the caller's own plan")
	assert.Equal(t, billing.SubscriptionOnlyLinkedFirst, reason)
	assert.Empty(t, allowances.held, "linked-first funding must not draw the included allowance")
}
