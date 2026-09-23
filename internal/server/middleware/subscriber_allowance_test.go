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
}

func (s *stubEntitlements) Project(context.Context, entitlement.Entitlement) error { return nil }

func (s *stubEntitlements) Get(context.Context, entitlement.SubscriberID) (entitlement.Entitlement, error) {
	if s.err != nil {
		return entitlement.Entitlement{}, s.err
	}
	if !s.found {
		return entitlement.Entitlement{}, entitlement.ErrEntitlementNotFound
	}
	return s.current, nil
}

// stubAllowances answers the usage read; accounting commands are unused here.
type stubAllowances struct {
	billingConsumed int64
	weeklyConsumed  int64
	sixHourConsumed int64
	sixHourReserved int64
	usageErr        error
	exhausted       entitlement.PeriodKind
	exhaustedOnce   bool
	releaseOnRead   int
	finalizeOnRead  int
	usageReads      int
	reserveErr      error
	cancelOnReserve context.CancelFunc
	held            []entitlement.Reservation
	released        []string
	releaseCtxErr   error
}

func (s *stubAllowances) Reserve(context.Context, entitlement.Reservation) (entitlement.Action, error) {
	return entitlement.Action{}, nil
}

func (s *stubAllowances) ReserveWithinLimits(_ context.Context, reservation entitlement.Reservation) (entitlement.Action, error) {
	s.held = append(s.held, reservation)
	if s.cancelOnReserve != nil {
		s.cancelOnReserve()
	}
	if s.exhausted != "" {
		return entitlement.Action{}, entitlement.ExhaustedError{Period: s.exhausted}
	}
	if s.exhaustedOnce && len(s.held) == 1 {
		return entitlement.Action{}, entitlement.ExhaustedError{Period: entitlement.PeriodKindSixHour}
	}
	if s.reserveErr != nil {
		return entitlement.Action{}, s.reserveErr
	}
	return entitlement.Action{Reservation: reservation, State: entitlement.ActionStateReserved}, nil
}

func (s *stubAllowances) Finalize(context.Context, entitlement.Finalization) (entitlement.Action, error) {
	return entitlement.Action{}, nil
}

func (s *stubAllowances) Release(ctx context.Context, release entitlement.Release) (entitlement.Action, error) {
	s.released = append(s.released, release.ActionID)
	s.releaseCtxErr = ctx.Err()
	return entitlement.Action{State: entitlement.ActionStateReleased}, nil
}

func (s *stubAllowances) Usage(_ context.Context, _ entitlement.SubscriberID, billing, weekly, sixHour entitlement.Period) (entitlement.Usage, error) {
	s.usageReads++
	if s.usageErr != nil {
		return entitlement.Usage{}, s.usageErr
	}
	if s.releaseOnRead > 0 && s.usageReads >= s.releaseOnRead {
		s.sixHourReserved = 0
	}
	if s.finalizeOnRead > 0 && s.usageReads >= s.finalizeOnRead {
		s.sixHourReserved = 0
		s.sixHourConsumed = sixHourAllowance
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

// A caller with no entitlement at all is billed at API pricing, and its own
// covering plan still funds the turn before the organization does.
func TestWithSubscriberAllowance_ServesCoveringSubscriptionForApiPricing(t *testing.T) {
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
	assert.True(t, subscriptionOnly)
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
	assert.Equal(t, entitlement.SubscriberID(allowanceSubscriberID), coverage.SubscriberID)
	assert.Len(t, allowances.held, 1, "the included allowance funds a turn the caller's plan cannot cover")
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

// Included allowance is metered capacity Weave pays for, so an unspent one is
// drawn only after the caller's own plan cannot take the turn — on every plan,
// not just the one whose funding order was written first.
func TestWithSubscriberAllowance_UsesCoveringSubscriptionBeforeIncludedAllowance(t *testing.T) {
	for name, current := range map[string]entitlement.Entitlement{
		"boost": activeSubscriberEntitlement(),
		"max":   maxSubscriberEntitlement(),
	} {
		t.Run(name, func(t *testing.T) {
			entitlements := &stubEntitlements{current: current, found: true}
			allowances := &stubAllowances{}
			w, reached, coverage := runAllowanceMiddlewareWithAuth(
				t, entitlements, allowances, subscriberAPIKey(), "Bearer sk-ant-oat-abc123")

			assert.True(t, reached)
			assert.Equal(t, http.StatusOK, w.Code)
			assert.Empty(t, coverage.SubscriberID)
			assert.Empty(t, allowances.held, "a turn the caller's own plan covers holds no included capacity")
		})
	}
}

func TestWithSubscriberAllowance_HoldsUpperBoundBeforeDispatch(t *testing.T) {
	// Admission alone lets concurrent turns each pass on headroom only one of
	// them can afford, so the gate must draw the bound down before the request
	// is served, and return it once it has been.
	entitlements := &stubEntitlements{current: activeSubscriberEntitlement(), found: true}
	allowances := &stubAllowances{}
	w, reached, _ := runAllowanceMiddleware(t, entitlements, allowances, subscriberAPIKey())

	require.True(t, reached)
	assert.Equal(t, http.StatusOK, w.Code)
	require.Len(t, allowances.held, 1)
	hold := allowances.held[0]
	assert.Positive(t, hold.ReservedUsdMicros)
	assert.LessOrEqual(t, hold.ReservedUsdMicros, sixHourAllowance,
		"a bound larger than the window would refuse every turn on a small plan")
	assert.Equal(t, entitlement.CapacitySourceIncludedRouter, hold.CapacitySource)
	assert.Equal(t, []string{hold.ActionID}, allowances.released,
		"the bound is returned once the turn is served; settlement books its actual cost")
}

func TestWithSubscriberAllowance_HoldFitsRemainingHeadroom(t *testing.T) {
	// A bound drawn against the window's whole cap would exceed what is left
	// the moment a window carries any usage, refusing turns the allowance can
	// still pay for.
	spent := sixHourAllowance - 1_000
	entitlements := &stubEntitlements{current: activeSubscriberEntitlement(), found: true}
	allowances := &stubAllowances{sixHourConsumed: spent}
	w, reached, _ := runAllowanceMiddleware(t, entitlements, allowances, subscriberAPIKey())

	require.True(t, reached)
	assert.Equal(t, http.StatusOK, w.Code)
	require.Len(t, allowances.held, 1)
	assert.Equal(t, sixHourAllowance-spent, allowances.held[0].ReservedUsdMicros)
}

func TestWithSubscriberAllowance_HoldFitsRemainingWeeklyHeadroom(t *testing.T) {
	// The week is the narrower window once a burst has drawn it down, so the
	// bound must clamp to it rather than to the six-hour cap above it.
	spent := weeklyAllowance - 1_000
	entitlements := &stubEntitlements{current: activeSubscriberEntitlement(), found: true}
	allowances := &stubAllowances{weeklyConsumed: spent}
	w, reached, _ := runAllowanceMiddleware(t, entitlements, allowances, subscriberAPIKey())

	require.True(t, reached)
	assert.Equal(t, http.StatusOK, w.Code)
	require.Len(t, allowances.held, 1)
	assert.Equal(t, weeklyAllowance-spent, allowances.held[0].ReservedUsdMicros)
}

func TestWithSubscriberAllowance_RefusedReservationCarriesNoCoverage(t *testing.T) {
	// The caller's own subscription serves the turn after the reservation is
	// refused. Coverage stamped before the hold was confirmed would settle that
	// turn against the window that just refused to hold anything for it.
	entitlements := &stubEntitlements{current: activeSubscriberEntitlement(), found: true}
	allowances := &stubAllowances{exhausted: entitlement.PeriodKindSixHour}
	w, reached, coverage := runAllowanceMiddlewareWithAuth(
		t, entitlements, allowances, subscriberAPIKey(), "Bearer sk-ant-oat-abc123")

	require.True(t, reached)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Empty(t, coverage.SubscriberID)
}

func TestWithSubscriberAllowance_RetriesReservationRefusedByConcurrentHold(t *testing.T) {
	// Admission reads stale headroom once; the atomic reservation refuses it.
	// A fresh admission and reservation should keep the turn on the subscriber.
	entitlements := &stubEntitlements{current: activeSubscriberEntitlement(), found: true}
	allowances := &stubAllowances{exhaustedOnce: true}
	w, reached, coverage := runAllowanceMiddleware(t, entitlements, allowances, subscriberAPIKey())

	assert.True(t, reached)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, entitlement.SubscriberID(allowanceSubscriberID), coverage.SubscriberID)
	assert.Len(t, allowances.held, 2)
	assert.Len(t, allowances.released, 1)
}

func TestWithSubscriberAllowance_CanceledRetryDoesNotDispatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entitlements := &stubEntitlements{current: activeSubscriberEntitlement(), found: true}
	allowances := &stubAllowances{exhausted: entitlement.PeriodKindSixHour, cancelOnReserve: cancel}
	reached := false
	engine := gateServing(t, entitlements, allowances, func(c *gin.Context) {
		reached = true
		c.Status(http.StatusBadGateway)
	})

	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(ctx))

	require.ErrorIs(t, ctx.Err(), context.Canceled)
	assert.False(t, reached, "a canceled allowance retry must not reach downstream billing or proxy handlers")
	assert.Len(t, allowances.held, 1)
}

func TestWithSubscriberAllowance_WaitsForHeldCapacityToRelease(t *testing.T) {
	entitlements := &stubEntitlements{current: activeSubscriberEntitlement(), found: true}
	allowances := &stubAllowances{sixHourReserved: sixHourAllowance, releaseOnRead: 2}
	w, reached, coverage := runAllowanceMiddleware(t, entitlements, allowances, subscriberAPIKey())

	assert.True(t, reached)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, entitlement.SubscriberID(allowanceSubscriberID), coverage.SubscriberID)
	assert.GreaterOrEqual(t, allowances.usageReads, 2)
	assert.Len(t, allowances.held, 1)
	assert.Len(t, allowances.released, 1)
}

func TestWithSubscriberAllowance_RefusesOrganizationBillingWhileAllowanceHeld(t *testing.T) {
	allowances := &stubAllowances{sixHourReserved: sixHourAllowance}
	recorder, reached := runSubscriberOrganizationBillingChain(t, allowances, &stubBillingRepo{balance: 100_000_000})

	assert.False(t, reached)
	assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "subscription_capacity_busy")
	assert.Equal(t, "1", recorder.Header().Get("Retry-After"))
	assert.Greater(t, allowances.usageReads, 1)
}

func TestWithSubscriberAllowance_UsesOrganizationBillingAfterConcurrentHoldSettlesAtLimit(t *testing.T) {
	allowances := &stubAllowances{sixHourReserved: sixHourAllowance, finalizeOnRead: 2}
	recorder, reached := runSubscriberOrganizationBillingChain(t, allowances, &stubBillingRepo{balance: 100_000_000})

	assert.True(t, reached)
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.GreaterOrEqual(t, allowances.usageReads, 2)
	assert.Empty(t, allowances.held)
}

func TestWithSubscriberAllowance_503WhenReservationFails(t *testing.T) {
	entitlements := &stubEntitlements{current: activeSubscriberEntitlement(), found: true}
	allowances := &stubAllowances{reserveErr: errors.New("accounting write failed")}
	w, reached, _ := runAllowanceMiddleware(t, entitlements, allowances, subscriberAPIKey())

	assert.False(t, reached, "an unwritable reservation fails closed rather than serving unbilled usage")
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}
