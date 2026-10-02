package middleware_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/server/middleware"
	"weave-os/router/internal/subscriptions/entitlement"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	allowanceSubscriberID = "11111111-1111-1111-1111-111111111111"
	monthlyAllowance      = int64(50_000_000)
	// The cap of the window covering allowanceNow, derived from the nominal
	// allowance, the 124 fixed windows the March 2026 period intersects, and
	// the burst factor.
	sixHourAllowance = 4 * int64(403_226)
	// The cap of the week covering allowanceNow: the period holds five.
	weeklyAllowance = int64(10_000_000)
)

var allowanceNow = time.Date(2026, 3, 14, 9, 30, 0, 0, time.UTC)

func subscriberAPIKey() *auth.APIKey {
	return &auth.APIKey{
		ID:                  "33333333-3333-3333-3333-333333333333",
		CredentialSubjectID: allowanceSubscriberID,
	}
}

// stubEntitlements answers the projection read WithSubscriberAllowance makes.
type stubEntitlements struct {
	current entitlement.Entitlement
	found   bool
	err     error
	observe func(entitlement.SubscriberID)
}

func (s *stubEntitlements) Project(context.Context, entitlement.Entitlement) error { return nil }

func (s *stubEntitlements) Get(_ context.Context, subscriberID entitlement.SubscriberID) (entitlement.Entitlement, error) {
	if s.observe != nil {
		s.observe(subscriberID)
	}
	if s.err != nil {
		return entitlement.Entitlement{}, s.err
	}
	if !s.found {
		return entitlement.Entitlement{}, entitlement.ErrEntitlementNotFound
	}
	return s.current, nil
}

// stubAllowances answers the usage read and counts accounting writes, which
// the gate must never make: settlement books each turn after it serves.
type stubAllowances struct {
	billingConsumed int64
	weeklyConsumed  int64
	sixHourConsumed int64
	sixHourReserved int64
	usageErr        error
	usageReads      int
	writes          int
}

func (s *stubAllowances) Reserve(context.Context, entitlement.Reservation) (entitlement.Action, error) {
	s.writes++
	return entitlement.Action{}, nil
}

func (s *stubAllowances) Finalize(context.Context, entitlement.Finalization) (entitlement.Action, error) {
	s.writes++
	return entitlement.Action{}, nil
}

func (s *stubAllowances) Release(context.Context, entitlement.Release) (entitlement.Action, error) {
	s.writes++
	return entitlement.Action{}, nil
}

func (s *stubAllowances) Usage(_ context.Context, _ entitlement.SubscriberID, billing, weekly, sixHour entitlement.Period) (entitlement.Usage, error) {
	s.usageReads++
	if s.usageErr != nil {
		return entitlement.Usage{}, s.usageErr
	}
	return entitlement.Usage{
		Billing: entitlement.WindowUsage{Period: billing, FinalizedUsdMicros: s.billingConsumed},
		Weekly:  entitlement.WindowUsage{Period: weekly, FinalizedUsdMicros: s.weeklyConsumed},
		SixHour: entitlement.WindowUsage{Period: sixHour, ReservedUsdMicros: s.sixHourReserved, FinalizedUsdMicros: s.sixHourConsumed},
	}, nil
}

func activeSubscriberEntitlement() entitlement.Entitlement {
	return entitlement.Entitlement{
		SubscriberID: entitlement.SubscriberID(allowanceSubscriberID),
		Version:      3,
		Plan:         entitlement.PlanBoost,
		Status:       entitlement.StatusActive,
		BillingPeriod: entitlement.Period{
			Kind:  entitlement.PeriodKindBilling,
			Start: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
			End:   time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		},
		EffectiveAt:                      time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		MonthlyAllowanceUsdMicros:        monthlyAllowance,
		NominalMonthlyAllowanceUsdMicros: monthlyAllowance,
		SixHourAllowanceUsdMicros:        403_225,
		ProjectedAt:                      time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
	}
}

// runAllowanceMiddleware drives WithSubscriberAllowance with a pre-stashed API
// key, the way WithAuth would leave one, and reports the coverage the handler
// downstream of the gate observed.
func runAllowanceMiddleware(
	t *testing.T,
	entitlements *stubEntitlements,
	allowances *stubAllowances,
	apiKey *auth.APIKey,
) (*httptest.ResponseRecorder, bool, entitlement.Coverage) {
	return runAllowanceMiddlewareWithAuth(t, entitlements, allowances, apiKey, "")
}

// runAllowanceMiddlewareWithAuth additionally sets an Authorization header, the
// way a caller presenting its own Claude/Codex subscription credential would.
func runAllowanceMiddlewareWithAuth(
	t *testing.T,
	entitlements *stubEntitlements,
	allowances *stubAllowances,
	apiKey *auth.APIKey,
	authHeader string,
) (*httptest.ResponseRecorder, bool, entitlement.Coverage) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	svc := entitlement.NewService(entitlements, allowances).WithClock(func() time.Time { return allowanceNow })

	reached := false
	var observed entitlement.Coverage
	engine := gin.New()
	engine.POST("/v1/messages", func(c *gin.Context) {
		if apiKey != nil {
			c.Set("router_api_key", apiKey)
		}
		middleware.WithSubscriberAllowance(svc)(c)
		if c.IsAborted() {
			return
		}
		reached = true
		observed, _ = entitlement.CoverageFromContext(c.Request.Context())
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	engine.ServeHTTP(w, req)
	return w, reached, observed
}

func TestWithSubscriberAllowance_PassesThroughNonSubscribers(t *testing.T) {
	for name, apiKey := range map[string]*auth.APIKey{
		"unauthenticated request":              nil,
		"key with no credential subject":       {ID: "33333333-3333-3333-3333-333333333333"},
		"key whose subject has no entitlement": subscriberAPIKey(),
	} {
		t.Run(name, func(t *testing.T) {
			w, reached, coverage := runAllowanceMiddleware(t, &stubEntitlements{}, &stubAllowances{}, apiKey)
			assert.True(t, reached, "a request with no individual entitlement keeps the billing path it already had")
			assert.Equal(t, http.StatusOK, w.Code)
			assert.Empty(t, coverage.SubscriberID, "no coverage may be attached, or the org would be billed nothing")
		})
	}
}

// A credential subject without a Weave entitlement keeps the organization's
// ordinary billing and routing path even when Claude Code presents OAuth.
func TestWithSubscriberAllowance_ClaudeOAuthDoesNotRestrictNonSubscriber(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := entitlement.NewService(&stubEntitlements{}, &stubAllowances{}).WithClock(func() time.Time { return allowanceNow })

	reached := false
	subscriptionOnly := false
	engine := gin.New()
	engine.POST("/v1/messages", func(c *gin.Context) {
		c.Set("router_api_key", subscriberAPIKey())
		middleware.WithSubscriberAllowance(svc)(c)
		if c.IsAborted() {
			return
		}
		reached = true
		subscriptionOnly = billing.SubscriptionOnlyFromContext(c.Request.Context())
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer sk-ant-oat-abc123")
	engine.ServeHTTP(httptest.NewRecorder(), req)

	require.True(t, reached)
	assert.False(t, subscriptionOnly)
}

// A credential that cannot serve the route leaves the turn on its ordinary
// billing path: a Codex bearer can't take /v1/messages.
func TestWithSubscriberAllowance_IgnoresSubscriptionThatCannotServeRoute(t *testing.T) {
	entitlements := &stubEntitlements{current: activeSubscriberEntitlement(), found: true}
	allowances := &stubAllowances{}
	w, reached, coverage := runAllowanceMiddlewareWithAuth(
		t, entitlements, allowances, subscriberAPIKey(), "Bearer sk-proj-codex-abc123")

	require.True(t, reached)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, entitlement.SubscriberID(allowanceSubscriberID), coverage.SubscriberID,
		"the included allowance funds a turn the caller's plan cannot cover")
}

func TestWithSubscriberAllowance_AttachesCoverageForActiveSubscriber(t *testing.T) {
	entitlements := &stubEntitlements{current: activeSubscriberEntitlement(), found: true}
	w, reached, coverage := runAllowanceMiddleware(t, entitlements, &stubAllowances{billingConsumed: 1_000}, subscriberAPIKey())

	require.True(t, reached)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, entitlement.SubscriberID(allowanceSubscriberID), coverage.SubscriberID)
	assert.Equal(t, int64(3), coverage.EntitlementVersion)
	assert.Equal(t, entitlement.PlanBoost, coverage.Plan)
	assert.Equal(t, monthlyAllowance, coverage.BillingLimitUsdMicros)
	assert.Equal(t, sixHourAllowance, coverage.SixHourLimitUsdMicros)
	assert.Equal(t, weeklyAllowance, coverage.WeeklyLimitUsdMicros)
	assert.Equal(t, time.Date(2026, 3, 14, 6, 0, 0, 0, time.UTC), coverage.SixHourPeriod.Start)
	assert.Equal(t, time.Date(2026, 3, 8, 0, 0, 0, 0, time.UTC), coverage.WeeklyPeriod.Start)
}

func TestWithSubscriberAllowance_UsesOrganizationFallbackWhenWindowSpent(t *testing.T) {
	for name, testCase := range map[string]struct {
		allowances *stubAllowances
	}{
		"billing month spent": {
			allowances: &stubAllowances{billingConsumed: monthlyAllowance},
		},
		"six-hour window spent": {
			allowances: &stubAllowances{sixHourConsumed: sixHourAllowance},
		},
		"weekly window spent": {
			allowances: &stubAllowances{weeklyConsumed: weeklyAllowance},
		},
	} {
		t.Run(name, func(t *testing.T) {
			entitlements := &stubEntitlements{current: activeSubscriberEntitlement(), found: true}
			w, reached, _ := runAllowanceMiddleware(t, entitlements, testCase.allowances, subscriberAPIKey())

			assert.True(t, reached)
			require.Equal(t, http.StatusOK, w.Code)
		})
	}
}

func TestWithSubscriberAllowance_UsesOrganizationFallbackAfterIncludedAllowance(t *testing.T) {
	entitlements := &stubEntitlements{current: activeSubscriberEntitlement(), found: true}
	allowances := &stubAllowances{billingConsumed: monthlyAllowance}
	w, reached, coverage := runAllowanceMiddleware(t, entitlements, allowances, subscriberAPIKey())

	require.True(t, reached)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Empty(t, coverage.SubscriberID)
}

func runSubscriberOrganizationBillingChain(
	t *testing.T,
	allowances *stubAllowances,
	repository *stubBillingRepo,
) (*httptest.ResponseRecorder, bool) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	entitlements := &stubEntitlements{current: activeSubscriberEntitlement(), found: true}
	allowanceService := entitlement.NewService(entitlements, allowances).WithClock(func() time.Time { return allowanceNow })
	billingService := billing.NewService(repository)
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		withInstallation(c, "trusted-org")
		c.Set("router_api_key", subscriberAPIKey())
		c.Next()
	})
	engine.Use(middleware.WithSubscriberAllowance(allowanceService))
	engine.Use(middleware.WithBalanceCheck(billingService, billing.MinBalanceMicros))
	engine.Use(middleware.WithAPIKeySpendCap(billingService))
	engine.Use(middleware.WithOrgMonthlySpendCap(billingService))
	reached := false
	engine.POST("/v1/messages", func(c *gin.Context) {
		reached = true
		c.Status(http.StatusOK)
	})

	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
	return recorder, reached
}

func TestSubscriberOrganizationBillingChain(t *testing.T) {
	for name, testCase := range map[string]struct {
		allowances    *stubAllowances
		repository    *stubBillingRepo
		expectedError string
		reached       bool
	}{
		"included allowance bypasses paid balance and caps": {
			allowances: &stubAllowances{},
			repository: &stubBillingRepo{
				balance:       0,
				spendFound:    true,
				spendMicros:   1,
				capMicros:     capPtr(1),
				orgMonthSpent: 1,
				orgMonthLimit: capPtr(1),
			},
			reached: true,
		},
		"exhausted allowance spends trusted organization balance": {
			allowances: &stubAllowances{billingConsumed: monthlyAllowance},
			repository: &stubBillingRepo{balance: 1, spendFound: true},
			reached:    true,
		},
		"exhausted allowance respects empty wallet": {
			allowances:    &stubAllowances{billingConsumed: monthlyAllowance},
			repository:    &stubBillingRepo{balance: 0},
			expectedError: "insufficient_credits",
		},
		"exhausted allowance respects key cap": {
			allowances: &stubAllowances{billingConsumed: monthlyAllowance},
			repository: &stubBillingRepo{
				balance:     1,
				spendFound:  true,
				spendMicros: 1,
				capMicros:   capPtr(1),
			},
			expectedError: "key_spend_cap_reached",
		},
		"exhausted allowance respects organization cap": {
			allowances: &stubAllowances{billingConsumed: monthlyAllowance},
			repository: &stubBillingRepo{
				balance:       1,
				spendFound:    true,
				orgMonthSpent: 1,
				orgMonthLimit: capPtr(1),
			},
			expectedError: "org_monthly_spend_limit_reached",
		},
	} {
		t.Run(name, func(t *testing.T) {
			recorder, reached := runSubscriberOrganizationBillingChain(t, testCase.allowances, testCase.repository)
			assert.Equal(t, testCase.reached, reached)
			if testCase.expectedError != "" {
				assert.Equal(t, http.StatusPaymentRequired, recorder.Code)
				assert.Contains(t, recorder.Body.String(), testCase.expectedError)
			}
			if len(testCase.repository.balanceOrgIDs) > 0 {
				assert.Equal(t, []string{"trusted-org"}, testCase.repository.balanceOrgIDs)
			}
			for _, organizationID := range testCase.repository.orgMonthOrgIDs {
				assert.Equal(t, "trusted-org", organizationID)
			}
		})
	}
}

func TestWithSubscriberAllowance_ExhaustedServesOnOrganizationCredits(t *testing.T) {
	gin.SetMode(gin.TestMode)
	entitlements := &stubEntitlements{current: activeSubscriberEntitlement(), found: true}
	allowances := &stubAllowances{billingConsumed: monthlyAllowance}
	svc := entitlement.NewService(entitlements, allowances).WithClock(func() time.Time { return allowanceNow })
	reached := false
	engine := gin.New()
	engine.POST("/v1/messages", func(c *gin.Context) {
		c.Set("router_api_key", subscriberAPIKey())
		middleware.WithSubscriberAllowance(svc)(c)
		if c.IsAborted() {
			return
		}
		reached = true
		c.Status(http.StatusOK)
	})
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))

	assert.True(t, reached, "a spent allowance falls back to organization credits")
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestWithSubscriberAllowance_503WhenAllowanceUnreadable(t *testing.T) {
	readFailure := errors.New("database unreachable")

	for name, testCase := range map[string]struct {
		entitlements *stubEntitlements
		allowances   *stubAllowances
	}{
		"projection unreadable": {entitlements: &stubEntitlements{err: readFailure}, allowances: &stubAllowances{}},
		"usage unreadable": {
			entitlements: &stubEntitlements{current: activeSubscriberEntitlement(), found: true},
			allowances:   &stubAllowances{usageErr: readFailure},
		},
	} {
		t.Run(name, func(t *testing.T) {
			w, reached, _ := runAllowanceMiddleware(t, testCase.entitlements, testCase.allowances, subscriberAPIKey())

			assert.False(t, reached, "an unreadable allowance fails closed rather than serving unbilled usage")
			require.Equal(t, http.StatusServiceUnavailable, w.Code)

			var body struct {
				Error string `json:"error"`
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
			assert.Equal(t, "billing_unavailable", body.Error)
		})
	}
}

func TestWithSubscriberAllowance_PassesThroughCoveringSubscription(t *testing.T) {
	// The caller presents its own Claude subscription on /v1/messages: that turn
	// serves on their plan at $0 and draws no included Router capacity, so a
	// spent Max/Boost allowance must not 402 it, and it must carry no coverage
	// (which would settle it against the allowance it never used).
	entitlements := &stubEntitlements{current: activeSubscriberEntitlement(), found: true}
	allowances := &stubAllowances{billingConsumed: monthlyAllowance, weeklyConsumed: weeklyAllowance, sixHourConsumed: sixHourAllowance}
	w, reached, coverage := runAllowanceMiddlewareWithAuth(
		t, entitlements, allowances, subscriberAPIKey(), "Bearer sk-ant-oat-abc123")

	assert.True(t, reached, "a turn served on the caller's own subscription is not bounded by the allowance")
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Empty(t, coverage.SubscriberID)
}

func TestWithSubscriberAllowance_BoostUsesCoveringSubscriptionBeforeIncludedAllowance(t *testing.T) {
	entitlements := &stubEntitlements{current: activeSubscriberEntitlement(), found: true}
	allowances := &stubAllowances{}
	w, reached, coverage := runAllowanceMiddlewareWithAuth(
		t, entitlements, allowances, subscriberAPIKey(), "Bearer sk-ant-oat-abc123")

	assert.True(t, reached)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, entitlement.SubscriberID(allowanceSubscriberID), coverage.SubscriberID,
		"included capacity must remain available if the linked subscription cannot serve the selected model")
	assert.Zero(t, allowances.writes, "admission must not charge the included allowance")
}

func TestWithSubscriberAllowance_CoversTurnWithoutReservingBeforeDispatch(t *testing.T) {
	// Concurrent agents must not wait on or refuse each other, so the gate
	// claims nothing while a turn is in flight — even with almost nothing
	// left in the window. Settlement books the turn's actual cost after it
	// serves.
	entitlements := &stubEntitlements{current: activeSubscriberEntitlement(), found: true}
	allowances := &stubAllowances{sixHourConsumed: sixHourAllowance - 1}
	w, reached, coverage := runAllowanceMiddleware(t, entitlements, allowances, subscriberAPIKey())

	require.True(t, reached)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, entitlement.SubscriberID(allowanceSubscriberID), coverage.SubscriberID)
	assert.Zero(t, allowances.writes)
}

func TestWithSubscriberAllowance_UsesOrganizationBillingOnceSettlementHoldsFillWindow(t *testing.T) {
	// A reserved amount is a settling turn's actual cost, so it spends the
	// window the same as a finalized one; the next turn moves on rather than
	// waiting for it to finalize.
	allowances := &stubAllowances{sixHourReserved: sixHourAllowance}
	recorder, reached := runSubscriberOrganizationBillingChain(t, allowances, &stubBillingRepo{balance: 100_000_000})

	assert.True(t, reached)
	assert.Equal(t, http.StatusOK, recorder.Code)
}

// A key can be handed around an organization, so the allowance is spent by the
// person the request identified itself as, not by the key's own owner.
func TestWithSubscriberAllowance_ChargesTheResolvedCaller(t *testing.T) {
	gin.SetMode(gin.TestMode)
	callerSubscriberID := "22222222-2222-2222-2222-222222222222"
	entitlements := &stubEntitlements{}
	svc := entitlement.NewService(entitlements, &stubAllowances{}).WithClock(func() time.Time { return allowanceNow })

	var metered entitlement.SubscriberID
	engine := gin.New()
	engine.POST("/v1/messages", func(c *gin.Context) {
		c.Set("router_api_key", subscriberAPIKey())
		c.Set("router_subscription_owner", auth.SubscriptionOwner{
			SubscriberID: callerSubscriberID,
			APIKeyID:     subscriberAPIKey().ID,
		})
		entitlements.observe = func(id entitlement.SubscriberID) { metered = id }
		middleware.WithSubscriberAllowance(svc)(c)
		c.Status(http.StatusOK)
	})

	engine.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/messages", nil))

	assert.Equal(t, entitlement.SubscriberID(callerSubscriberID), metered)
}
