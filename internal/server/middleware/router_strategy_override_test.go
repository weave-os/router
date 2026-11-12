package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/server/middleware"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

// runStrategyOverride reports the strategy observed on the request context after WithRouterStrategyOverride runs.
// allRegisteredLive stands in for a deployment whose every registered lane has a loaded snapshot.
func allRegisteredLive(router.Strategy) bool { return true }

func TestClassifierCannotBeSelectedWithoutThreadAdmission(t *testing.T) {
	installation := overrideEnabledInstallation()
	installation.RoutingStrategy = router.StrategyLLMClassifier
	for _, header := range []string{"", string(router.StrategyLLMClassifier)} {
		probe := probeStrategyOverrideWithHeader(t, installation, false, header, router.StrategyHMMEmbedding, router.StrategyLLMClassifier, router.StrategyHMMEmbedding)
		assert.Equal(t, http.StatusServiceUnavailable, probe.status)
	}
	header := overrideEnabledInstallation()
	observed := runStrategyOverride(t, header, string(router.StrategyLLMClassifier), router.StrategyLLMClassifier, router.StrategyHMMEmbedding)
	assert.Equal(t, router.StrategyHMMEmbedding, observed)
	assert.Empty(t, middleware.NormalizeRouterStrategyDefault(router.StrategyLLMClassifier, router.StrategyLLMClassifier))
}

func runStrategyOverride(t *testing.T, installation *auth.Installation, header string, available ...router.Strategy) router.Strategy {
	t.Helper()
	return runStrategyOverrideWithAvailability(t, installation, header, allRegisteredLive, available...)
}

func runStrategyOverrideWithAvailability(
	t *testing.T,
	installation *auth.Installation,
	header string,
	liveAvailability middleware.StrategyAvailability,
	available ...router.Strategy,
) router.Strategy {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		if installation != nil {
			c.Set("router_installation", installation)
		}
		c.Next()
	})
	engine.Use(middleware.WithRouterStrategyDefault(router.StrategyHMMEmbedding, liveAvailability, available...))

	var observed router.Strategy
	engine.GET("/probe", func(c *gin.Context) {
		observed = router.StrategyFromContext(c.Request.Context())
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	if header != "" {
		req.Header.Set(middleware.RouterStrategyOverrideHeader, header)
	}
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)
	return observed
}

func TestRouterStrategyOverride_AppliesRL(t *testing.T) {
	got := runStrategyOverride(t, overrideEnabledInstallation(), "rl")
	assert.Equal(t, router.StrategyRL, got, "rl header must select the RL strategy")
}

func TestRouterStrategyOverride_AppliesHMM(t *testing.T) {
	got := runStrategyOverride(t, overrideEnabledInstallation(), "hmm")
	assert.Equal(t, router.StrategyHMM, got, "hmm header must select the HMM strategy")
}

func TestRouterStrategyOverride_AppliesHMMEmbedding(t *testing.T) {
	got := runStrategyOverride(t, overrideEnabledInstallation(), "hmm_embedding")
	assert.Equal(t, router.StrategyHMMEmbedding, got)
}

func TestRouterStrategyOverride_CaseInsensitiveAndTrimmed(t *testing.T) {
	got := runStrategyOverride(t, overrideEnabledInstallation(), "  RL  ")
	assert.Equal(t, router.StrategyRL, got, "value must be lowercased and trimmed before matching")
}

func TestRouterStrategyOverride_NoHeaderUsesDeploymentDefault(t *testing.T) {
	got := runStrategyOverride(t, &auth.Installation{ID: "inst-eval"}, "")
	assert.Equal(t, router.StrategyHMMEmbedding, got)
}

func TestRouterStrategyOverride_ClusterHeaderIgnored(t *testing.T) {
	for _, tc := range []struct {
		persisted router.Strategy
		expected  router.Strategy
	}{
		{persisted: "", expected: router.StrategyHMMEmbedding},
		{persisted: router.StrategyHMM, expected: router.StrategyHMM},
	} {
		installation := overrideEnabledInstallation()
		installation.RoutingStrategy = tc.persisted
		got := runStrategyOverride(t, installation, string(router.StrategyCluster))
		assert.Equal(t, tc.expected, got)
	}
}

func TestRouterStrategyOverride_UsesDeploymentDefaultWithoutInstallationOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set("router_installation", &auth.Installation{ID: "inst-global"})
		c.Next()
	})
	engine.Use(middleware.WithRouterStrategyDefault(
		router.StrategyHMM,
		nil,
		router.StrategyHMM,
	))
	var observed router.Strategy
	engine.GET("/probe", func(c *gin.Context) {
		observed = router.StrategyFromContext(c.Request.Context())
		c.Status(http.StatusOK)
	})

	request := httptest.NewRequest(http.MethodGet, "/probe", nil)
	engine.ServeHTTP(httptest.NewRecorder(), request)

	assert.Equal(t, router.StrategyHMM, observed)
}

func TestRouterStrategyOverride_PersistedClusterFollowsDeploymentDefault(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set("router_installation", &auth.Installation{
			ID: "inst-cluster-holdout", RoutingStrategy: router.StrategyCluster,
		})
		c.Next()
	})
	engine.Use(middleware.WithRouterStrategyDefault(
		router.StrategyHMM,
		nil,
		router.StrategyHMM,
	))
	var observed router.Strategy
	engine.GET("/probe", func(c *gin.Context) {
		observed = router.StrategyFromContext(c.Request.Context())
		c.Status(http.StatusOK)
	})

	request := httptest.NewRequest(http.MethodGet, "/probe", nil)
	engine.ServeHTTP(httptest.NewRecorder(), request)

	assert.Equal(t, router.StrategyHMM, observed)
}

func TestNormalizeRouterStrategyDefault(t *testing.T) {
	assert.Equal(t, router.StrategyHMM, middleware.NormalizeRouterStrategyDefault(router.StrategyHMM, router.StrategyHMM))
	assert.Empty(t, middleware.NormalizeRouterStrategyDefault(router.Strategy("typo"), router.StrategyHMM))
	assert.Empty(t, middleware.NormalizeRouterStrategyDefault(router.StrategyCluster, router.StrategyHMM))
	assert.Empty(t,
		middleware.NormalizeRouterStrategyDefault(router.StrategyHMMBeta, router.StrategyHMMBeta),
		"beta cannot be activated by a deployment default")
}

func TestRouterStrategyOverride_AuthorizedBetaHeaderActivatesBeta(t *testing.T) {
	got := runStrategyOverride(
		t,
		&auth.Installation{ID: "inst-beta", PolicyHeaderOverridesEnabled: true},
		string(router.StrategyHMMBeta),
		router.StrategyHMMBeta,
		router.StrategyHMMEmbedding,
	)
	assert.Equal(t, router.StrategyHMMBeta, got)
}

func TestRouterStrategyOverride_PersistedInstallationBetaActivatesBeta(t *testing.T) {
	got := runStrategyOverride(
		t,
		&auth.Installation{ID: "inst-beta", RoutingStrategy: router.StrategyHMMBeta},
		"",
		router.StrategyHMMBeta,
	)
	assert.Equal(t, router.StrategyHMMBeta, got)
}

func TestRouterStrategyOverride_PersistedBetaFailsClosedWhenBetaUnregistered(t *testing.T) {
	probe := probeStrategyOverride(t, &auth.Installation{ID: "inst-beta", RoutingStrategy: router.StrategyHMMBeta}, false,
		router.StrategyHMMEmbedding, router.StrategyHMM, router.StrategyHMMEmbedding)

	assert.Equal(t, http.StatusServiceUnavailable, probe.status, "a beta lane that failed to load must not be selectable")
	assert.False(t, probe.reached)
}

func TestRouterStrategyOverride_PersistedBetaFailsClosedWhileBetaLaneEmpty(t *testing.T) {
	betaLaneEmpty := func(strategy router.Strategy) bool { return strategy != router.StrategyHMMBeta }
	probe := probeStrategyOverrideWith(t, &auth.Installation{ID: "inst-beta", RoutingStrategy: router.StrategyHMMBeta}, false, "",
		router.StrategyHMMEmbedding, betaLaneEmpty, router.StrategyHMMEmbedding, router.StrategyHMMBeta)

	assert.Equal(t, http.StatusServiceUnavailable, probe.status, "a registered beta lane with no snapshot must not be selectable")
	assert.False(t, probe.reached)
}

func TestRouterStrategyOverride_BetaHeaderIgnoredWhileBetaLaneEmpty(t *testing.T) {
	betaLaneEmpty := func(strategy router.Strategy) bool { return strategy != router.StrategyHMMBeta }
	got := runStrategyOverrideWithAvailability(
		t,
		&auth.Installation{ID: "inst-beta", RoutingStrategy: router.StrategyHMM, PolicyHeaderOverridesEnabled: true},
		string(router.StrategyHMMBeta),
		betaLaneEmpty,
		router.StrategyHMM,
		router.StrategyHMMBeta,
	)
	assert.Equal(t, router.StrategyHMM, got)
}

func TestRouterStrategyOverride_BetaUnselectableWithoutAvailabilitySource(t *testing.T) {
	probe := probeStrategyOverrideWith(t, &auth.Installation{ID: "inst-beta", RoutingStrategy: router.StrategyHMMBeta}, false, "",
		router.StrategyHMMEmbedding, nil, router.StrategyHMMEmbedding, router.StrategyHMMBeta)

	assert.Equal(t, http.StatusServiceUnavailable, probe.status)
	assert.False(t, probe.reached)
}

func TestRouterStrategyOverride_DeploymentDefaultBetaFailsClosed(t *testing.T) {
	probe := probeStrategyOverride(t, &auth.Installation{ID: "inst-global"}, false, router.StrategyHMMBeta, router.StrategyHMMBeta)

	assert.Equal(t, http.StatusServiceUnavailable, probe.status, "beta cannot be activated by a deployment default")
	assert.False(t, probe.reached)
}

func TestRouterStrategyOverride_UnknownValueIgnored(t *testing.T) {
	got := runStrategyOverride(t, overrideEnabledInstallation(), "bogus")
	assert.Equal(t, router.StrategyHMMEmbedding, got, "unrecognized strategy must fall through to the default")
}

func TestRouterStrategyOverride_AppliesPersistedStrategyWithoutHeader(t *testing.T) {
	got := runStrategyOverride(t, &auth.Installation{ID: "inst-allowlisted", RoutingStrategy: router.StrategyHMM}, "")
	assert.Equal(t, router.StrategyHMM, got)
}

func TestRouterStrategyOverride_NormalizesPersistedStrategy(t *testing.T) {
	got := runStrategyOverride(t, &auth.Installation{ID: "inst-allowlisted", RoutingStrategy: router.Strategy("  HMM  ")}, "")
	assert.Equal(t, router.StrategyHMM, got)
}

func TestRouterStrategyOverride_UnauthorizedHeaderPreservesPersistedStrategy(t *testing.T) {
	got := runStrategyOverride(t, &auth.Installation{ID: "inst-customer", RoutingStrategy: router.StrategyHMM}, "rl")
	assert.Equal(t, router.StrategyHMM, got)
}

func TestRouterStrategyOverride_AcceptsFutureRegisteredStrategy(t *testing.T) {
	future := router.Strategy("future-policy")
	installation := overrideEnabledInstallation()
	got := runStrategyOverride(t, installation, string(future), future, router.StrategyHMMEmbedding)
	assert.Equal(t, future, got)
}

func TestRouterStrategyOverride_NoOpsWhenInstallationMissing(t *testing.T) {
	got := runStrategyOverride(t, nil, "rl")
	assert.Equal(t, router.StrategyHMMEmbedding, got, "missing installation (WithAuth bypassed) must not flip strategy")
}

func overrideEnabledInstallation() *auth.Installation {
	return &auth.Installation{ID: "inst-eval", PolicyHeaderOverridesEnabled: true}
}

type strategyProbe struct {
	status   int
	body     string
	observed router.Strategy
	reached  bool
}

func probeStrategyOverride(t *testing.T, installation *auth.Installation, managedServing bool, defaultStrategy router.Strategy, available ...router.Strategy) strategyProbe {
	t.Helper()
	return probeStrategyOverrideWithHeader(t, installation, managedServing, "", defaultStrategy, available...)
}

func probeStrategyOverrideWithHeader(t *testing.T, installation *auth.Installation, managedServing bool, header string, defaultStrategy router.Strategy, available ...router.Strategy) strategyProbe {
	t.Helper()
	return probeStrategyOverrideWith(t, installation, managedServing, header, defaultStrategy, allRegisteredLive, available...)
}

func probeStrategyOverrideWith(t *testing.T, installation *auth.Installation, managedServing bool, header string, defaultStrategy router.Strategy, liveAvailability middleware.StrategyAvailability, available ...router.Strategy) strategyProbe {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set("router_installation", installation)
		if managedServing {
			ctx := requestcontext.WithServingIdentity(c.Request.Context(), requestcontext.ServingIdentity{Target: "prod/stable"})
			c.Request = c.Request.WithContext(ctx)
		}
		c.Next()
	})
	engine.Use(middleware.WithRouterStrategyDefault(defaultStrategy, liveAvailability, available...))
	probe := strategyProbe{}
	engine.GET("/probe", func(c *gin.Context) {
		probe.reached = true
		probe.observed = router.StrategyFromContext(c.Request.Context())
		c.Status(http.StatusOK)
	})

	request := httptest.NewRequest(http.MethodGet, "/probe", nil)
	if header != "" {
		request.Header.Set(middleware.RouterStrategyOverrideHeader, header)
	}
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	probe.status = response.Code
	probe.body = response.Body.String()
	return probe
}

func TestRouterStrategyOverride_ManagedServingUnavailablePersistedStrategyFailsClosed(t *testing.T) {
	installation := &auth.Installation{ID: "inst-managed", RoutingStrategy: router.StrategyRL}
	probe := probeStrategyOverride(t, installation, true, router.StrategyHMMEmbedding, router.StrategyHMMEmbedding)

	assert.Equal(t, http.StatusServiceUnavailable, probe.status)
	assert.JSONEq(t, `{"error":"routing_strategy_unavailable"}`, probe.body)
	assert.False(t, probe.reached)
}

func TestRouterStrategyOverride_ManagedServingRemapsRetiredBetaToDeploymentDefault(t *testing.T) {
	for _, persisted := range []router.Strategy{router.StrategyHMMBeta, ""} {
		t.Run(string(persisted), func(t *testing.T) {
			installation := &auth.Installation{ID: "inst-remap", RoutingStrategy: persisted}
			probe := probeStrategyOverride(t, installation, true, router.StrategyHMMEmbedding, router.StrategyHMM, router.StrategyHMMEmbedding)

			assert.Equal(t, http.StatusOK, probe.status)
			assert.Equal(t, router.StrategyHMMEmbedding, probe.observed)
		})
	}
}

func TestRouterStrategyOverride_ManagedServingRemappedDefaultUnavailableFailsClosed(t *testing.T) {
	installation := &auth.Installation{ID: "inst-remap-unavailable", RoutingStrategy: router.StrategyHMMBeta}
	probe := probeStrategyOverride(t, installation, true, router.StrategyHMMBeta, router.StrategyHMM)

	assert.Equal(t, http.StatusServiceUnavailable, probe.status)
	assert.False(t, probe.reached)
}

func TestRouterStrategyOverride_NonManagedUnavailableStrategyFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name            string
		persisted       router.Strategy
		defaultStrategy router.Strategy
	}{
		{name: "persisted", persisted: router.StrategyRL, defaultStrategy: router.StrategyHMMEmbedding},
		{name: "default", persisted: "", defaultStrategy: router.StrategyHMM},
		{name: "persisted cluster with unregistered default", persisted: router.StrategyCluster, defaultStrategy: router.StrategyHMM},
	} {
		t.Run(tc.name, func(t *testing.T) {
			installation := &auth.Installation{ID: "inst-self-hosted", RoutingStrategy: tc.persisted}
			probe := probeStrategyOverride(t, installation, false, tc.defaultStrategy, router.StrategyHMMEmbedding)

			assert.Equal(t, http.StatusServiceUnavailable, probe.status)
			assert.JSONEq(t, `{"error":"routing_strategy_unavailable"}`, probe.body)
			assert.False(t, probe.reached)
		})
	}
}
