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
	// allowance and the 124 fixed windows the March 2026 period intersects.
	sixHourAllowance = int64(403_226)
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
	sixHourConsumed int64
	usageErr        error
	exhausted       entitlement.PeriodKind
	reserveErr      error
	held            []entitlement.Reservation
	released        []string
	releaseCtxErr   error
}

type stubSubscriberPrepaid struct {
	authorizeErr   error
	authorizations []billing.PrepaidAuthorizationRequest
	finalized      []string
}

func (s *stubSubscriberPrepaid) Balance(context.Context, billing.Owner) (int64, error) {
	return 1_000_000, nil
}

func (s *stubSubscriberPrepaid) Debit(context.Context, billing.PrepaidDebit) (int64, error) {
	panic("subscriber allowance middleware must authorize before dispatch")
}

func (s *stubSubscriberPrepaid) Authorize(_ context.Context, request billing.PrepaidAuthorizationRequest) (billing.PrepaidAuthorization, error) {
	s.authorizations = append(s.authorizations, request)
	if s.authorizeErr != nil {
		return billing.PrepaidAuthorization{}, s.authorizeErr
	}
	return billing.PrepaidAuthorization{
		Owner:             request.Owner,
		ActionID:          request.ActionID,
		RouterRequestID:   request.RouterRequestID,
		APIKeyID:          request.APIKeyID,
		RequestedModel:    request.RequestedModel,
		ReservedUsdMicros: request.UpperBoundUsdMicros,
		State:             billing.PrepaidAuthorizationReserved,
		CapacitySource:    entitlement.CapacitySourcePrepaid,
	}, nil
}

func (s *stubSubscriberPrepaid) Settle(context.Context, billing.PrepaidSettlement) (int64, error) {
	return 0, nil
}

func (s *stubSubscriberPrepaid) Finalize(_ context.Context, actionID string) (int64, error) {
	s.finalized = append(s.finalized, actionID)
	return 1_000_000, nil
}

func (s *stubAllowances) Reserve(context.Context, entitlement.Reservation) (entitlement.Action, error) {
	return entitlement.Action{}, nil
}

func (s *stubAllowances) ReserveWithinLimits(_ context.Context, reservation entitlement.Reservation) (entitlement.Action, error) {
	s.held = append(s.held, reservation)
	if s.exhausted != "" {
		return entitlement.Action{}, entitlement.ExhaustedError{Period: s.exhausted}
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

func (s *stubAllowances) Usage(_ context.Context, _ entitlement.SubscriberID, billing, sixHour entitlement.Period) (entitlement.Usage, error) {
	if s.usageErr != nil {
		return entitlement.Usage{}, s.usageErr
	}
	return entitlement.Usage{
		Billing: entitlement.WindowUsage{Period: billing, FinalizedUsdMicros: s.billingConsumed},
		SixHour: entitlement.WindowUsage{Period: sixHour, FinalizedUsdMicros: s.sixHourConsumed},
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
		middleware.WithSubscriberAllowance(svc, nil)(c)
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
	assert.Equal(t, time.Date(2026, 3, 14, 6, 0, 0, 0, time.UTC), coverage.SixHourPeriod.Start)
}

func TestWithSubscriberAllowance_402WhenWindowSpent(t *testing.T) {
	for name, testCase := range map[string]struct {
		allowances   *stubAllowances
		expectedKind string
		expectedEnd  time.Time
	}{
		"billing month spent": {
			allowances:   &stubAllowances{billingConsumed: monthlyAllowance},
			expectedKind: string(entitlement.PeriodKindBilling),
			expectedEnd:  time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		},
		"six-hour window spent": {
			allowances:   &stubAllowances{sixHourConsumed: sixHourAllowance},
			expectedKind: string(entitlement.PeriodKindSixHour),
			expectedEnd:  time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC),
		},
	} {
		t.Run(name, func(t *testing.T) {
			entitlements := &stubEntitlements{current: activeSubscriberEntitlement(), found: true}
			w, reached, _ := runAllowanceMiddleware(t, entitlements, testCase.allowances, subscriberAPIKey())

			assert.False(t, reached, "a spent allowance must not route onto paid capacity")
			require.Equal(t, http.StatusPaymentRequired, w.Code)

			var body struct {
				Error              string    `json:"error"`
				PeriodKind         string    `json:"period_kind"`
				PeriodEnd          time.Time `json:"period_end"`
				ConsumedUSDMicros  int64     `json:"consumed_usd_micros"`
				AllowanceUSDMicros int64     `json:"allowance_usd_micros"`
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
			assert.Equal(t, "subscription_allowance_exhausted", body.Error)
			assert.Equal(t, testCase.expectedKind, body.PeriodKind)
			assert.Equal(t, testCase.expectedEnd, body.PeriodEnd.UTC(), "the client is told when the window it hit reopens")
			assert.Equal(t, body.AllowanceUSDMicros, body.ConsumedUSDMicros)
		})
	}
}

func TestWithSubscriberAllowance_UsesPrepaidAfterIncludedAllowance(t *testing.T) {
	entitlements := &stubEntitlements{current: activeSubscriberEntitlement(), found: true}
	allowances := &stubAllowances{billingConsumed: monthlyAllowance}
	prepaid := &stubSubscriberPrepaid{}
	billingSvc := billing.NewService(nil).WithSubscriberPrepaid(prepaid)
	var observed billing.PrepaidAuthorization

	w, reached := runAllowanceMiddlewareWithBilling(t, entitlements, allowances, billingSvc, func(ctx context.Context) {
		observed, _ = billing.PrepaidAuthorizationFromContext(ctx)
	})

	require.True(t, reached)
	assert.Equal(t, http.StatusOK, w.Code)
	require.Len(t, prepaid.authorizations, 1)
	assert.Equal(t, billing.SubscriberOwner(allowanceSubscriberID), prepaid.authorizations[0].Owner)
	assert.Equal(t, entitlement.ModelUnresolved, prepaid.authorizations[0].RequestedModel)
	assert.Equal(t, prepaid.authorizations[0].ActionID, observed.ActionID)
	assert.Equal(t, []string{observed.ActionID}, prepaid.finalized)
}

func TestWithSubscriberAllowance_DoesNotAuthorizePrepaidWhileIncludedCapacityIsAvailable(t *testing.T) {
	entitlements := &stubEntitlements{current: activeSubscriberEntitlement(), found: true}
	prepaid := &stubSubscriberPrepaid{}
	billingSvc := billing.NewService(nil).WithSubscriberPrepaid(prepaid)

	w, reached := runAllowanceMiddlewareWithBilling(t, entitlements, &stubAllowances{}, billingSvc, nil)

	require.True(t, reached)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Empty(t, prepaid.authorizations)
}

func TestWithSubscriberAllowance_MapsPrepaidAuthorizationFailures(t *testing.T) {
	for name, testCase := range map[string]struct {
		err        error
		statusCode int
		bodyError  string
	}{
		"insufficient funds": {
			err:        billing.ErrInsufficientCredits,
			statusCode: http.StatusPaymentRequired,
			bodyError:  "insufficient_subscriber_credits",
		},
		"storage failure": {
			err:        errors.New("database unavailable"),
			statusCode: http.StatusServiceUnavailable,
			bodyError:  "billing_unavailable",
		},
	} {
		t.Run(name, func(t *testing.T) {
			entitlements := &stubEntitlements{current: activeSubscriberEntitlement(), found: true}
			prepaid := &stubSubscriberPrepaid{authorizeErr: testCase.err}
			billingSvc := billing.NewService(nil).WithSubscriberPrepaid(prepaid)

			w, reached := runAllowanceMiddlewareWithBilling(
				t,
				entitlements,
				&stubAllowances{billingConsumed: monthlyAllowance},
				billingSvc,
				nil,
			)

			assert.False(t, reached)
			assert.Equal(t, testCase.statusCode, w.Code)
			var body struct {
				Error string `json:"error"`
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
			assert.Equal(t, testCase.bodyError, body.Error)
		})
	}
}

func runAllowanceMiddlewareWithBilling(
	t *testing.T,
	entitlements *stubEntitlements,
	allowances *stubAllowances,
	billingSvc *billing.Service,
	observe func(context.Context),
) (*httptest.ResponseRecorder, bool) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	svc := entitlement.NewService(entitlements, allowances).WithClock(func() time.Time { return allowanceNow })
	reached := false
	engine := gin.New()
	engine.POST("/v1/messages", func(c *gin.Context) {
		c.Set("router_api_key", subscriberAPIKey())
		middleware.WithSubscriberAllowance(svc, billingSvc)(c)
		if c.IsAborted() {
			return
		}
		reached = true
		if observe != nil {
			observe(c.Request.Context())
		}
		c.Status(http.StatusOK)
	})
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
	return w, reached
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
	allowances := &stubAllowances{billingConsumed: monthlyAllowance, sixHourConsumed: sixHourAllowance}
	w, reached, coverage := runAllowanceMiddlewareWithAuth(
		t, entitlements, allowances, subscriberAPIKey(), "Bearer sk-ant-oat-abc123")

	assert.True(t, reached, "a turn served on the caller's own subscription is not bounded by the allowance")
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Empty(t, coverage.SubscriberID)
}

func TestWithSubscriberAllowance_CoversCoveringSubscriptionWithAllowanceLeft(t *testing.T) {
	// Presenting a subscription credential only means the turn *may* serve on
	// the caller's plan. While allowance remains the request is still admitted
	// with coverage, so a turn Weave ends up serving meters against the windows
	// instead of silently falling through to the organization's balance.
	entitlements := &stubEntitlements{current: activeSubscriberEntitlement(), found: true}
	w, reached, coverage := runAllowanceMiddlewareWithAuth(
		t, entitlements, &stubAllowances{}, subscriberAPIKey(), "Bearer sk-ant-oat-abc123")

	assert.True(t, reached)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, entitlement.SubscriberID(allowanceSubscriberID), coverage.SubscriberID)
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

func TestWithSubscriberAllowance_402WhenReservationRefused(t *testing.T) {
	// The windows read as unspent, so only the reservation's own gate can
	// refuse this turn — the concurrency case the hold exists for.
	entitlements := &stubEntitlements{current: activeSubscriberEntitlement(), found: true}
	allowances := &stubAllowances{exhausted: entitlement.PeriodKindSixHour}
	w, reached, _ := runAllowanceMiddleware(t, entitlements, allowances, subscriberAPIKey())

	assert.False(t, reached, "a refused reservation must not dispatch work the allowance cannot pay for")
	require.Equal(t, http.StatusPaymentRequired, w.Code)

	var body struct {
		Error      string `json:"error"`
		PeriodKind string `json:"period_kind"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "subscription_allowance_exhausted", body.Error)
	assert.Equal(t, string(entitlement.PeriodKindSixHour), body.PeriodKind)
	assert.Empty(t, allowances.released, "a refused reservation leaves nothing to release")
}

func TestWithSubscriberAllowance_503WhenReservationFails(t *testing.T) {
	entitlements := &stubEntitlements{current: activeSubscriberEntitlement(), found: true}
	allowances := &stubAllowances{reserveErr: errors.New("accounting write failed")}
	w, reached, _ := runAllowanceMiddleware(t, entitlements, allowances, subscriberAPIKey())

	assert.False(t, reached, "an unwritable reservation fails closed rather than serving unbilled usage")
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}
