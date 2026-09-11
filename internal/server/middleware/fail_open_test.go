package middleware_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/server/middleware"
)

// Composition-root shape from cmd/router/main.go, reproduced so the tests
// exercise the same preparer/starter contract the binary wires.
func failOpenPreparation(health *requestcontext.DependencyHealth) (auth.RequestPreparer, auth.DependencyStarter) {
	limits := requestcontext.DefaultPreparationLimits()
	preparer := auth.RequestPreparer(func(ctx context.Context) context.Context {
		prepared, _, _ := requestcontext.BeginPreparation(ctx, health, limits)
		return prepared
	})
	starter := auth.DependencyStarter(func(ctx context.Context) (context.Context, func(error), error) {
		return requestcontext.StartDependency(ctx, requestcontext.DependencyDatabase)
	})
	return preparer, starter
}

type recordingSubscriptionAccountRepository struct {
	failingSubscriptionAccountRepository
	accounts []*auth.SubscriptionAccount
	calls    int
	fail     bool
}

func (r *recordingSubscriptionAccountRepository) ListSubscriptionAccounts(context.Context, string) ([]*auth.SubscriptionAccount, error) {
	r.calls++
	if r.fail {
		return nil, r.err
	}
	return r.accounts, nil
}

func authProbe(t *testing.T, svc *auth.Service, token string, onProbe func(*gin.Context)) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(middleware.WithAuth(svc, false))
	engine.GET("/probe", func(c *gin.Context) {
		if onProbe != nil {
			onProbe(c)
		}
		c.Status(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set(middleware.RouterKeyHeader, token)
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)
	return rr
}

func TestWithAuthServesRecentKeyDuringDatabaseOutage(t *testing.T) {
	const routerToken = "rk_fail_open_known"
	hash, prefix, suffix := auth.APITokenFingerprint(routerToken)
	apiKey := &auth.APIKey{ID: "key-fail-open", KeyHash: hash, KeyPrefix: prefix, KeySuffix: suffix}
	installation := &auth.Installation{ID: "inst-fail-open", ExternalID: "ext-fail-open"}
	repo := &fakeAPIKeyRepository{byHash: map[string]fakeKeyRow{hash: {apiKey: apiKey, installation: installation}}}
	cache := auth.NewLRUAPIKeyCache(8, 8, time.Millisecond, time.Millisecond)
	preparer, starter := failOpenPreparation(requestcontext.NewDependencyHealth())
	svc := auth.NewService(fakeInstallationRepository{}, repo, nil, nil, cache, nil, time.Now).
		WithDependencyPreparation(preparer, starter)

	require.Equal(t, http.StatusOK, authProbe(t, svc, routerToken, nil).Code, "warm the cache with a successful read")
	// The positive LRU expires almost immediately; only the outage copy remains.
	time.Sleep(5 * time.Millisecond)

	repo.lookupErr = errors.New("connection refused")
	var served *auth.Installation
	rr := authProbe(t, svc, routerToken, func(c *gin.Context) { served = middleware.InstallationFrom(c) })
	require.Equal(t, http.StatusOK, rr.Code, "a recently verified key keeps working while the database is down")
	require.NotNil(t, served)
	assert.Equal(t, installation.ID, served.ID)
}

func TestWithAuthStaysClosedWithoutARecentKeyOrPreparation(t *testing.T) {
	const routerToken = "rk_fail_open_cold"
	hash, prefix, suffix := auth.APITokenFingerprint(routerToken)
	apiKey := &auth.APIKey{ID: "key-cold", KeyHash: hash, KeyPrefix: prefix, KeySuffix: suffix}
	installation := &auth.Installation{ID: "inst-cold"}

	t.Run("cold cache", func(t *testing.T) {
		repo := &fakeAPIKeyRepository{byHash: map[string]fakeKeyRow{}, lookupErr: errors.New("connection refused")}
		preparer, starter := failOpenPreparation(requestcontext.NewDependencyHealth())
		svc := auth.NewService(fakeInstallationRepository{}, repo, nil, nil, auth.NewLRUAPIKeyCache(8, 8, time.Minute, time.Minute), nil, time.Now).
			WithDependencyPreparation(preparer, starter)
		rr := authProbe(t, svc, routerToken, nil)
		assert.Equal(t, http.StatusServiceUnavailable, rr.Code, "an unknown key never falls open")
	})

	t.Run("switch off", func(t *testing.T) {
		repo := &fakeAPIKeyRepository{byHash: map[string]fakeKeyRow{hash: {apiKey: apiKey, installation: installation}}}
		cache := auth.NewLRUAPIKeyCache(8, 8, time.Millisecond, time.Millisecond)
		svc := auth.NewService(fakeInstallationRepository{}, repo, nil, nil, cache, nil, time.Now)
		require.Equal(t, http.StatusOK, authProbe(t, svc, routerToken, nil).Code)
		time.Sleep(5 * time.Millisecond)
		repo.lookupErr = errors.New("connection refused")
		assert.Equal(t, http.StatusServiceUnavailable, authProbe(t, svc, routerToken, nil).Code, "legacy behavior is unchanged when the switch is off")
	})

	t.Run("revoked installation", func(t *testing.T) {
		repo := &fakeAPIKeyRepository{byHash: map[string]fakeKeyRow{hash: {apiKey: apiKey, installation: installation}}}
		cache := auth.NewLRUAPIKeyCache(8, 8, time.Minute, time.Minute)
		preparer, starter := failOpenPreparation(requestcontext.NewDependencyHealth())
		svc := auth.NewService(fakeInstallationRepository{}, repo, nil, nil, cache, nil, time.Now).
			WithDependencyPreparation(preparer, starter)
		require.Equal(t, http.StatusOK, authProbe(t, svc, routerToken, nil).Code)
		cache.InvalidateInstallation(installation.ID)
		repo.lookupErr = errors.New("connection refused")
		assert.Equal(t, http.StatusServiceUnavailable, authProbe(t, svc, routerToken, nil).Code, "explicit invalidation also clears the outage copy")
	})
}

func TestWithAuthReusesEnrollmentSnapshotDuringDatabaseOutage(t *testing.T) {
	const routerToken = "rk_fail_open_enrollment"
	hash, prefix, suffix := auth.APITokenFingerprint(routerToken)
	apiKey := &auth.APIKey{ID: "key-enrollment", KeyHash: hash, KeyPrefix: prefix, KeySuffix: suffix}
	repo := &fakeAPIKeyRepository{byHash: map[string]fakeKeyRow{hash: {apiKey: apiKey, installation: &auth.Installation{ID: "inst-enrollment"}}}}
	subscriptions := &recordingSubscriptionAccountRepository{
		failingSubscriptionAccountRepository: failingSubscriptionAccountRepository{err: errors.New("database unavailable")},
		accounts:                             []*auth.SubscriptionAccount{{ID: "acct-1", APIKeyID: apiKey.ID, Provider: auth.SubscriptionProviderClaude}},
	}
	preparer, starter := failOpenPreparation(requestcontext.NewDependencyHealth())
	svc := auth.NewService(fakeInstallationRepository{}, repo, nil, nil, auth.NewLRUAPIKeyCache(8, 8, time.Minute, time.Minute), nil, time.Now).
		WithSubscriptionAccounts(subscriptions).
		WithDependencyPreparation(preparer, starter)

	unavailableFlag := func(c *gin.Context) bool {
		unavailable, _ := c.Request.Context().Value(proxy.ManagedSubscriptionEnrollmentUnavailableContextKey{}).(bool)
		return unavailable
	}
	var firstUnavailable, secondUnavailable bool
	require.Equal(t, http.StatusOK, authProbe(t, svc, routerToken, func(c *gin.Context) { firstUnavailable = unavailableFlag(c) }).Code)
	assert.False(t, firstUnavailable)
	require.Equal(t, 1, subscriptions.calls)

	subscriptions.fail = true
	require.Equal(t, http.StatusOK, authProbe(t, svc, routerToken, func(c *gin.Context) { secondUnavailable = unavailableFlag(c) }).Code)
	assert.False(t, secondUnavailable, "a recent enrollment snapshot stands in for the failed read")
	assert.Equal(t, 2, subscriptions.calls, "the live read is still attempted first")
}

// billingGateProbe drives one billing gate with a synthetic installation and
// api key. prepared controls whether the request carries an active preparation.
func billingGateProbe(t *testing.T, gate gin.HandlerFunc, prepared bool) (*httptest.ResponseRecorder, bool) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	reached := false
	engine.GET("/probe", func(c *gin.Context) {
		withInstallation(c, "ext-gate")
		c.Set("router_api_key", &auth.APIKey{ID: "key-gate"})
		if prepared {
			ctx, _, _ := requestcontext.BeginPreparation(c.Request.Context(), requestcontext.NewDependencyHealth(), requestcontext.DefaultPreparationLimits())
			c.Request = c.Request.WithContext(ctx)
		}
		gate(c)
		if c.IsAborted() {
			return
		}
		reached = true
		c.Status(http.StatusOK)
	})
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/probe", nil))
	return w, reached
}

func TestBillingGatesServeRecentReadOnlyDuringPreparedOutage(t *testing.T) {
	capMicros := int64(5_000_000)
	for _, tt := range []struct {
		name string
		gate func(*billing.Service, *middleware.BillingFailOpenCache) gin.HandlerFunc
		fail func(*stubBillingRepo)
	}{
		{
			name: "balance",
			gate: func(svc *billing.Service, cache *middleware.BillingFailOpenCache) gin.HandlerFunc {
				return middleware.WithBalanceCheckAndFailOpen(svc, billing.MinBalanceMicros, cache)
			},
			fail: func(r *stubBillingRepo) { r.balanceErr = errors.New("connection refused") },
		},
		{
			name: "api key cap",
			gate: middleware.WithAPIKeySpendCapAndFailOpen,
			fail: func(r *stubBillingRepo) { r.spendErr = errors.New("connection refused") },
		},
		{
			name: "org monthly cap",
			gate: middleware.WithOrgMonthlySpendCapAndFailOpen,
			fail: func(r *stubBillingRepo) { r.orgMonthErr = errors.New("connection refused") },
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo := &stubBillingRepo{balance: 10_000_000, spendMicros: 1_000_000, capMicros: &capMicros, spendFound: true, orgMonthSpent: 1_000_000, orgMonthLimit: &capMicros}
			svc := billing.NewService(repo)
			cache := middleware.NewBillingFailOpenCache()
			gate := tt.gate(svc, cache)

			_, reached := billingGateProbe(t, gate, true)
			require.True(t, reached, "healthy read admits the request and records the snapshot")

			tt.fail(repo)
			_, reached = billingGateProbe(t, gate, true)
			assert.True(t, reached, "the recent snapshot admits the request during a prepared outage")

			_, reached = billingGateProbe(t, gate, false)
			assert.False(t, reached, "without preparation the gate keeps its strict behavior")

			cold := tt.gate(svc, middleware.NewBillingFailOpenCache())
			_, reached = billingGateProbe(t, cold, true)
			assert.False(t, reached, "a cold cache never admits a request it has not seen succeed")
		})
	}
}
