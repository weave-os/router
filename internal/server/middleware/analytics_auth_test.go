package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/server/middleware"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func analyticsAuthEngine(t *testing.T, rawToken string, scope auth.APIKeyScope, handler gin.HandlerFunc) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	hash, prefix, suffix := auth.APITokenFingerprint(rawToken)
	installation := &auth.Installation{ID: "inst-analytics", ExternalID: "ext-analytics"}
	repo := &fakeAPIKeyRepository{byHash: map[string]fakeKeyRow{
		hash: {
			apiKey: &auth.APIKey{
				ID: "key-analytics", InstallationID: installation.ID,
				KeyHash: hash, KeyPrefix: prefix, KeySuffix: suffix, Scope: scope,
			},
			installation: installation,
		},
	}}
	svc := auth.NewService(fakeInstallationRepository{}, repo, nil, nil, auth.NoOpAPIKeyCache{}, nil, func() time.Time {
		return time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC)
	})

	engine := gin.New()
	engine.Use(middleware.WithAnalyticsKey(svc))
	engine.GET("/probe", handler)
	return engine
}

func analyticsProbe(t *testing.T, engine *gin.Engine, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)
	return rr
}

func TestWithAnalyticsKeyAdmitsAnalyticsScopedKey(t *testing.T) {
	const token = "ra_export"
	var sawInstallation string
	engine := analyticsAuthEngine(t, token, auth.ScopeAnalyticsRead, func(c *gin.Context) {
		installation := middleware.InstallationFrom(c)
		require.NotNil(t, installation, "the export handler needs the authed installation to scope its query")
		sawInstallation = installation.ID
		c.Status(http.StatusOK)
	})

	rr := analyticsProbe(t, engine, token)

	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "inst-analytics", sawInstallation)
}

func TestWithAnalyticsKeyRejectsRoutingKey(t *testing.T) {
	const token = "rk_routing"
	engine := analyticsAuthEngine(t, token, auth.ScopeRouting, func(c *gin.Context) {
		t.Error("a routing key must never reach the export handler")
	})

	rr := analyticsProbe(t, engine, token)

	require.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestWithAnalyticsKeyRejectsMissingToken(t *testing.T) {
	engine := analyticsAuthEngine(t, "ra_export", auth.ScopeAnalyticsRead, func(c *gin.Context) {
		t.Error("an unauthenticated request must never reach the export handler")
	})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)

	require.Equal(t, http.StatusUnauthorized, rr.Code)
}

// The whole point of the scope split: the ETL credential cannot proxy inference.
func TestWithAuthRejectsAnalyticsKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const token = "ra_export"
	hash, prefix, suffix := auth.APITokenFingerprint(token)
	installation := &auth.Installation{ID: "inst-analytics", ExternalID: "ext-analytics"}
	repo := &fakeAPIKeyRepository{byHash: map[string]fakeKeyRow{
		hash: {
			apiKey: &auth.APIKey{
				ID: "key-analytics", InstallationID: installation.ID,
				KeyHash: hash, KeyPrefix: prefix, KeySuffix: suffix,
				Scope: auth.ScopeAnalyticsRead,
			},
			installation: installation,
		},
	}}
	svc := auth.NewService(fakeInstallationRepository{}, repo, nil, nil, auth.NoOpAPIKeyCache{}, nil, func() time.Time { return time.Now() })

	engine := gin.New()
	engine.Use(middleware.WithAuth(svc, false))
	engine.POST("/v1/messages", func(c *gin.Context) {
		t.Error("an analytics key must never reach an inference route")
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set(middleware.RouterKeyHeader, token)
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)

	require.Equal(t, http.StatusUnauthorized, rr.Code)
}

func readKeyService(t *testing.T, installation *auth.Installation, keys map[string]auth.APIKeyScope) *auth.Service {
	t.Helper()
	repo := &fakeAPIKeyRepository{byHash: map[string]fakeKeyRow{}}
	for token, scope := range keys {
		hash, prefix, suffix := auth.APITokenFingerprint(token)
		repo.byHash[hash] = fakeKeyRow{
			apiKey: &auth.APIKey{
				ID: "key-" + token, InstallationID: installation.ID,
				KeyHash: hash, KeyPrefix: prefix, KeySuffix: suffix, Scope: scope,
			},
			installation: installation,
		}
	}
	return auth.NewService(fakeInstallationRepository{}, repo, nil, nil, auth.NoOpAPIKeyCache{}, nil, func() time.Time {
		return time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	})
}

func TestWithReadKeyResolvesTheSameInstallationForRoutingAndAnalyticsKeys(t *testing.T) {
	gin.SetMode(gin.TestMode)
	installation := &auth.Installation{ID: "inst-read", ExternalID: "ext-read"}
	svc := readKeyService(t, installation, map[string]auth.APIKeyScope{
		"rk_routing":    auth.ScopeRouting,
		"ra_analytics":  auth.ScopeAnalyticsRead,
		"rk_mislabeled": auth.ScopeAnalyticsRead,
		"ra_mislabeled": auth.ScopeRouting,
	})
	var sawInstallation, sawKey string
	engine := gin.New()
	engine.Use(middleware.WithReadKey(svc))
	engine.GET("/probe", func(c *gin.Context) {
		sawInstallation = middleware.InstallationFrom(c).ID
		sawKey = middleware.APIKeyFrom(c).ID
		c.Status(http.StatusOK)
	})

	for _, test := range []struct {
		name, header, token string
		want                int
	}{
		{"routing key as bearer", "Authorization", "Bearer rk_routing", http.StatusOK},
		{"analytics key as bearer", "Authorization", "Bearer ra_analytics", http.StatusOK},
		{"routing key as x-api-key", "X-API-Key", "rk_routing", http.StatusOK},
		{"analytics key as x-api-key", "X-API-Key", "ra_analytics", http.StatusOK},
		{"unknown or revoked key", "Authorization", "Bearer rk_revoked", http.StatusUnauthorized},
		{"analytics scope behind routing prefix", "Authorization", "Bearer rk_mislabeled", http.StatusUnauthorized},
		{"routing scope behind analytics prefix", "Authorization", "Bearer ra_mislabeled", http.StatusUnauthorized},
		{"upstream provider key", "X-API-Key", "sk-ant-upstream", http.StatusUnauthorized},
	} {
		t.Run(test.name, func(t *testing.T) {
			sawInstallation, sawKey = "", ""
			req := httptest.NewRequest(http.MethodGet, "/probe", nil)
			req.Header.Set(test.header, test.token)
			rr := httptest.NewRecorder()
			engine.ServeHTTP(rr, req)

			require.Equal(t, test.want, rr.Code)
			if test.want == http.StatusOK {
				assert.Equal(t, "inst-read", sawInstallation)
				assert.NotEmpty(t, sawKey)
			} else {
				assert.Empty(t, sawInstallation, "a rejected key must never reach the handler")
			}
		})
	}
}
