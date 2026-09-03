package middleware_test

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"workweave/router/internal/auth"
	"workweave/router/internal/flags"
	"workweave/router/internal/proxy"
	"workweave/router/internal/router"
	"workweave/router/internal/server/middleware"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeExternalAPIKeyRepository struct {
	byInstallationID map[string][]*auth.ExternalAPIKey
}

func (f *fakeExternalAPIKeyRepository) Create(context.Context, auth.CreateExternalAPIKeyParams) (*auth.ExternalAPIKey, error) {
	return nil, errors.New("not used")
}

func (f *fakeExternalAPIKeyRepository) GetForInstallation(_ context.Context, installationID string) ([]*auth.ExternalAPIKey, error) {
	return f.byInstallationID[installationID], nil
}

func (f *fakeExternalAPIKeyRepository) SoftDeleteByProvider(context.Context, string, string) error {
	return errors.New("not used")
}

func (f *fakeExternalAPIKeyRepository) SoftDelete(context.Context, string, string) error {
	return errors.New("not used")
}

func (f *fakeExternalAPIKeyRepository) UpdateModelAliases(context.Context, string, string, map[string]string) (*auth.ExternalAPIKey, error) {
	return nil, errors.New("not used")
}

func (f *fakeExternalAPIKeyRepository) MarkUsed(context.Context, string) error {
	return nil
}

type fakeAPIKeyRepository struct {
	byHash map[string]fakeKeyRow
	// lookupErr stands in for an infra failure (pool exhausted, DB unreachable,
	// deadline elapsed) as opposed to a key that simply isn't there.
	lookupErr error
	mu        sync.Mutex
	used      []string
}

type fakeKeyRow struct {
	apiKey       *auth.APIKey
	installation *auth.Installation
}

func (f *fakeAPIKeyRepository) Create(ctx context.Context, params auth.CreateAPIKeyParams) (*auth.APIKey, error) {
	return nil, errors.New("not used")
}

func (f *fakeAPIKeyRepository) GetActiveByHashWithInstallation(ctx context.Context, keyHash string) (*auth.APIKey, *auth.Installation, error) {
	if f.lookupErr != nil {
		return nil, nil, f.lookupErr
	}
	row, ok := f.byHash[keyHash]
	if !ok {
		return nil, nil, sql.ErrNoRows
	}
	return row.apiKey, row.installation, nil
}

func (f *fakeAPIKeyRepository) ListForInstallation(ctx context.Context, installationID string) ([]*auth.APIKey, error) {
	return nil, errors.New("not used")
}

func (f *fakeAPIKeyRepository) MarkUsed(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.used = append(f.used, id)
	return nil
}

func (f *fakeAPIKeyRepository) SoftDelete(ctx context.Context, installationID, id string) (int64, error) {
	return 0, errors.New("not used")
}

type fakeInstallationRepository struct{}

type failingSubscriptionAccountRepository struct{ err error }

func (r failingSubscriptionAccountRepository) UpsertSubscriptionAccount(context.Context, auth.CreateSubscriptionAccountParams) (*auth.SubscriptionAccount, error) {
	return nil, r.err
}
func (r failingSubscriptionAccountRepository) ListSubscriptionAccounts(context.Context, string) ([]*auth.SubscriptionAccount, error) {
	return nil, r.err
}
func (r failingSubscriptionAccountRepository) UpdateSubscriptionAccountState(context.Context, string, string, bool, *time.Time) error {
	return r.err
}
func (r failingSubscriptionAccountRepository) UpdateSubscriptionAccountCooldown(context.Context, string, string, time.Time) error {
	return r.err
}
func (r failingSubscriptionAccountRepository) UpdateSubscriptionRefreshToken(context.Context, string, string, []byte) error {
	return r.err
}
func (r failingSubscriptionAccountRepository) DeleteSubscriptionAccount(context.Context, string, string) error {
	return r.err
}

func (fakeInstallationRepository) Create(ctx context.Context, params auth.CreateInstallationParams) (*auth.Installation, error) {
	return nil, errors.New("not used")
}

func (fakeInstallationRepository) Get(ctx context.Context, externalID, id string) (*auth.Installation, error) {
	return nil, errors.New("not used")
}

func (fakeInstallationRepository) ListForExternalID(ctx context.Context, externalID string) ([]*auth.Installation, error) {
	return nil, errors.New("not used")
}

func (fakeInstallationRepository) SoftDelete(ctx context.Context, externalID, id string) error {
	return errors.New("not used")
}

func (fakeInstallationRepository) MarkFirstRequestServed(ctx context.Context, id string) error {
	return nil
}

func (fakeInstallationRepository) UpdateExcludedModels(ctx context.Context, externalID, id string, models []string) error {
	return errors.New("not used")
}

func (fakeInstallationRepository) UpdateFastModeModels(ctx context.Context, externalID, id string, models []string) error {
	return errors.New("not used")
}

func (fakeInstallationRepository) UpdateAllowedModels(ctx context.Context, externalID, id string, models []string) error {
	return errors.New("not used")
}

func (fakeInstallationRepository) UpdateExcludedProviders(ctx context.Context, externalID, id string, providerNames []string) error {
	return errors.New("not used")
}

func (fakeInstallationRepository) UpdateRoutingPreference(ctx context.Context, externalID, id string, qualityWeight *float64) error {
	return errors.New("not used")
}
func (fakeInstallationRepository) UpdateUsageBypass(ctx context.Context, externalID, id string, enabled bool, threshold *float64) error {
	return errors.New("not used")
}
func (fakeInstallationRepository) UpdateSubscriptionRoutingDisabled(ctx context.Context, externalID, id string, disabled bool) error {
	return errors.New("not used")
}
func (fakeInstallationRepository) UpdateContentCaptureMode(ctx context.Context, externalID, id string, mode *string) error {
	return errors.New("not used")
}
func (fakeInstallationRepository) UpdateHideTerminalSurfaces(ctx context.Context, externalID, id string, hide bool) error {
	return errors.New("not used")
}

func (fakeInstallationRepository) UpdateFlagOverrides(ctx context.Context, externalID, id string, overrides flags.Overrides) error {
	return errors.New("not used")
}

func TestWithAuthPrefersRouterKeyHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const routerToken = "rk_router"
	hash, prefix, suffix := auth.APITokenFingerprint(routerToken)
	apiKey := &auth.APIKey{ID: "key-1", KeyHash: hash, KeyPrefix: prefix, KeySuffix: suffix}
	installation := &auth.Installation{
		ID: "inst-1", ExternalID: "ext-1", RoutingRolloutID: "rollout-1",
		PolicyShadowStrategy: "future-policy", PolicyDebugEnabled: true,
		PolicyRoutingIntent: "high", AITrainingAllowed: true,
	}
	repo := &fakeAPIKeyRepository{byHash: map[string]fakeKeyRow{
		hash: {apiKey: apiKey, installation: installation},
	}}
	svc := auth.NewService(fakeInstallationRepository{}, repo, nil, nil, auth.NoOpAPIKeyCache{}, nil, func() time.Time {
		return time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC)
	})

	engine := gin.New()
	engine.Use(middleware.WithAuth(svc, false))
	engine.GET("/probe", func(c *gin.Context) {
		assert.Equal(t, installation, middleware.InstallationFrom(c))
		assert.Equal(t, apiKey, middleware.APIKeyFrom(c))
		ctx := c.Request.Context()
		assert.Equal(t, "rollout-1", ctx.Value(proxy.PolicyRolloutIDContextKey{}))
		assert.Equal(t, router.Strategy("future-policy"), ctx.Value(proxy.PolicyShadowStrategyContextKey{}))
		assert.Equal(t, true, ctx.Value(proxy.PolicyDebugEnabledContextKey{}))
		assert.Equal(t, "high", ctx.Value(proxy.PolicyRoutingIntentContextKey{}))
		assert.Equal(t, true, ctx.Value(proxy.PolicyTrainingAllowedContextKey{}))
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set(middleware.RouterKeyHeader, routerToken)
	req.Header.Set("Authorization", "Bearer anthropic-oauth-token")
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
}

func TestWithAuthLeavesControlPlaneAvailableWhenSubscriptionEnrollmentLookupFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const routerToken = "rk_subscription_lookup"
	hash, prefix, suffix := auth.APITokenFingerprint(routerToken)
	apiKey := &auth.APIKey{ID: "key-subscription-lookup", KeyHash: hash, KeyPrefix: prefix, KeySuffix: suffix}
	repo := &fakeAPIKeyRepository{byHash: map[string]fakeKeyRow{
		hash: {apiKey: apiKey, installation: &auth.Installation{ID: "inst-1"}},
	}}
	svc := auth.NewService(fakeInstallationRepository{}, repo, nil, nil, auth.NoOpAPIKeyCache{}, nil, time.Now).
		WithSubscriptionAccounts(failingSubscriptionAccountRepository{err: errors.New("database unavailable")})

	engine := gin.New()
	engine.Use(middleware.WithAuth(svc, false))
	engine.GET("/validate", func(c *gin.Context) {
		unavailable, _ := c.Request.Context().Value(proxy.ManagedSubscriptionEnrollmentUnavailableContextKey{}).(bool)
		require.True(t, unavailable)
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/validate", nil)
	req.Header.Set(middleware.RouterKeyHeader, routerToken)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	require.Equal(t, http.StatusOK, recorder.Code)
}

func TestWithAuthPropagatesContentCaptureOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const routerToken = "rk_capture"
	hash, prefix, suffix := auth.APITokenFingerprint(routerToken)
	apiKey := &auth.APIKey{ID: "key-capture", KeyHash: hash, KeyPrefix: prefix, KeySuffix: suffix}
	mode := "off"
	installation := &auth.Installation{ID: "inst-capture", ExternalID: "ext-capture", ContentCaptureMode: &mode}
	repo := &fakeAPIKeyRepository{byHash: map[string]fakeKeyRow{
		hash: {apiKey: apiKey, installation: installation},
	}}
	svc := auth.NewService(fakeInstallationRepository{}, repo, nil, nil, auth.NoOpAPIKeyCache{}, nil, func() time.Time { return time.Now() })

	engine := gin.New()
	engine.Use(middleware.WithAuth(svc, false))
	engine.GET("/probe", func(c *gin.Context) {
		// Without this the proxy only ever sees the deployment-wide mode, and
		// an installation that asked for no retention still gets captured.
		assert.Equal(t, proxy.CaptureOff, c.Request.Context().Value(proxy.InstallationCaptureModeContextKey{}))
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set(middleware.RouterKeyHeader, routerToken)
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
}

func TestWithAuthOmitsContentCaptureOverrideWhenUnset(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const routerToken = "rk_nocapture"
	hash, prefix, suffix := auth.APITokenFingerprint(routerToken)
	apiKey := &auth.APIKey{ID: "key-nocapture", KeyHash: hash, KeyPrefix: prefix, KeySuffix: suffix}
	installation := &auth.Installation{ID: "inst-nocapture", ExternalID: "ext-nocapture"}
	repo := &fakeAPIKeyRepository{byHash: map[string]fakeKeyRow{
		hash: {apiKey: apiKey, installation: installation},
	}}
	svc := auth.NewService(fakeInstallationRepository{}, repo, nil, nil, auth.NoOpAPIKeyCache{}, nil, func() time.Time { return time.Now() })

	engine := gin.New()
	engine.Use(middleware.WithAuth(svc, false))
	engine.GET("/probe", func(c *gin.Context) {
		assert.Nil(t, c.Request.Context().Value(proxy.InstallationCaptureModeContextKey{}),
			"no stored override must leave the deployment mode in charge, not pin it to off")
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set(middleware.RouterKeyHeader, routerToken)
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
}

func TestWithAuthManagedModeDropsBYOKWhenNotOptedIn(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const routerToken = "rk_managed"
	hash, prefix, suffix := auth.APITokenFingerprint(routerToken)
	apiKey := &auth.APIKey{ID: "key-managed", InstallationID: "inst-managed", KeyHash: hash, KeyPrefix: prefix, KeySuffix: suffix}
	installation := &auth.Installation{ID: "inst-managed", ExternalID: "ext-managed", ByokEnabled: false}
	repo := &fakeAPIKeyRepository{byHash: map[string]fakeKeyRow{
		hash: {apiKey: apiKey, installation: installation},
	}}
	// VerifyAPIKey returns this row, so without the managed-mode gate the
	// middleware would stash it on the request context.
	externalRepo := &fakeExternalAPIKeyRepository{byInstallationID: map[string][]*auth.ExternalAPIKey{
		installation.ID: {{ID: "ext-leftover", InstallationID: installation.ID, Provider: "anthropic", Plaintext: []byte("sk-ant-leftover")}},
	}}
	svc := auth.NewService(fakeInstallationRepository{}, repo, externalRepo, nil, auth.NoOpAPIKeyCache{}, nil, func() time.Time { return time.Now() })

	engine := gin.New()
	engine.Use(middleware.WithAuth(svc, true))
	engine.GET("/probe", func(c *gin.Context) {
		assert.Nil(t, c.Request.Context().Value(proxy.ExternalAPIKeysContextKey{}),
			"managed mode must drop BYOK rows for an installation that hasn't opted in; a leftover row in the table must not reach the proxy ctx")
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set(middleware.RouterKeyHeader, routerToken)
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
}

func TestWithAuthManagedModeKeepsBYOKWhenOptedIn(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const routerToken = "rk_managed_optin"
	hash, prefix, suffix := auth.APITokenFingerprint(routerToken)
	apiKey := &auth.APIKey{ID: "key-optin", InstallationID: "inst-optin", KeyHash: hash, KeyPrefix: prefix, KeySuffix: suffix}
	installation := &auth.Installation{ID: "inst-optin", ExternalID: "ext-optin", ByokEnabled: true}
	repo := &fakeAPIKeyRepository{byHash: map[string]fakeKeyRow{
		hash: {apiKey: apiKey, installation: installation},
	}}
	externalRepo := &fakeExternalAPIKeyRepository{byInstallationID: map[string][]*auth.ExternalAPIKey{
		installation.ID: {{
			ID: "ext-makora", InstallationID: installation.ID, Provider: "makora",
			BaseURL: "https://byok.example.com/v1", Plaintext: []byte("mk-byok"),
		}},
	}}
	svc := auth.NewService(fakeInstallationRepository{}, repo, externalRepo, nil, auth.NoOpAPIKeyCache{}, nil, func() time.Time { return time.Now() })

	engine := gin.New()
	engine.Use(middleware.WithAuth(svc, true))
	engine.GET("/probe", func(c *gin.Context) {
		v, ok := c.Request.Context().Value(proxy.ExternalAPIKeysContextKey{}).([]*auth.ExternalAPIKey)
		require.True(t, ok, "managed mode must propagate BYOK rows once the installation opts in")
		require.Len(t, v, 1)
		assert.Equal(t, "makora", v[0].Provider)
		assert.Equal(t, "https://byok.example.com/v1", v[0].BaseURL)
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set(middleware.RouterKeyHeader, routerToken)
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
}

func TestWithAuthSelfHostedKeepsBYOKInContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const routerToken = "rk_selfhosted"
	hash, prefix, suffix := auth.APITokenFingerprint(routerToken)
	apiKey := &auth.APIKey{ID: "key-self", InstallationID: "inst-self", KeyHash: hash, KeyPrefix: prefix, KeySuffix: suffix}
	// ByokEnabled stays false: self-hosted has no credit system to protect, so
	// the opt-in flag must not gate BYOK there.
	installation := &auth.Installation{ID: "inst-self", ExternalID: "ext-self", ByokEnabled: false}
	repo := &fakeAPIKeyRepository{byHash: map[string]fakeKeyRow{
		hash: {apiKey: apiKey, installation: installation},
	}}
	externalRepo := &fakeExternalAPIKeyRepository{byInstallationID: map[string][]*auth.ExternalAPIKey{
		installation.ID: {{ID: "ext-byok", InstallationID: installation.ID, Provider: "anthropic", Plaintext: []byte("sk-ant-byok")}},
	}}
	svc := auth.NewService(fakeInstallationRepository{}, repo, externalRepo, nil, auth.NoOpAPIKeyCache{}, nil, func() time.Time { return time.Now() })

	engine := gin.New()
	engine.Use(middleware.WithAuth(svc, false))
	engine.GET("/probe", func(c *gin.Context) {
		v, ok := c.Request.Context().Value(proxy.ExternalAPIKeysContextKey{}).([]*auth.ExternalAPIKey)
		require.True(t, ok, "self-hosted mode must propagate BYOK rows to the proxy ctx")
		require.Len(t, v, 1)
		assert.Equal(t, "anthropic", v[0].Provider)
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set(middleware.RouterKeyHeader, routerToken)
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
}

func TestWithAuthSnapshotsForwardedClientHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const routerToken = "rk_snapshot"
	hash, prefix, suffix := auth.APITokenFingerprint(routerToken)
	apiKey := &auth.APIKey{ID: "key-snap", InstallationID: "inst-snap", KeyHash: hash, KeyPrefix: prefix, KeySuffix: suffix}
	installation := &auth.Installation{ID: "inst-snap", ExternalID: "ext-snap", ByokEnabled: true}
	repo := &fakeAPIKeyRepository{byHash: map[string]fakeKeyRow{
		hash: {apiKey: apiKey, installation: installation},
	}}
	externalRepo := &fakeExternalAPIKeyRepository{byInstallationID: map[string][]*auth.ExternalAPIKey{
		installation.ID: {{
			ID: "ext-gateway", InstallationID: installation.ID, Provider: "anthropic_gateway",
			ForwardedClientHeaders: []string{"X-SNOWFLAKE-APPLICATION"},
			BaggageHeader:          "X-SNOWFLAKE-BAGGAGE",
		}},
	}}
	svc := auth.NewService(fakeInstallationRepository{}, repo, externalRepo, nil, auth.NoOpAPIKeyCache{}, nil, func() time.Time { return time.Now() })

	engine := gin.New()
	engine.Use(middleware.WithAuth(svc, true))
	engine.GET("/probe", func(c *gin.Context) {
		// Router-built upstream calls (compaction summaries, Cortex web
		// search) have no inbound request to read these off later.
		snapshot := proxy.ForwardedHeaderSnapshotFrom(c.Request.Context())
		assert.Equal(t, "cortex-cli/1.2.3", snapshot.Get("X-SNOWFLAKE-APPLICATION"))
		assert.Equal(t, `{"deployment":"prod"}`, snapshot.Get("X-SNOWFLAKE-BAGGAGE"))
		assert.Empty(t, snapshot.Get("Authorization"),
			"only headers a key forwards belong in the snapshot")
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set(middleware.RouterKeyHeader, routerToken)
	req.Header.Set("X-SNOWFLAKE-APPLICATION", "cortex-cli/1.2.3")
	req.Header.Set("X-SNOWFLAKE-BAGGAGE", `{"deployment":"prod"}`)
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
}

func TestWithAuthKeepsLegacyBearerFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const routerToken = "rk_router"
	hash, prefix, suffix := auth.APITokenFingerprint(routerToken)
	apiKey := &auth.APIKey{ID: "key-1", KeyHash: hash, KeyPrefix: prefix, KeySuffix: suffix}
	installation := &auth.Installation{ID: "inst-1", ExternalID: "ext-1"}
	repo := &fakeAPIKeyRepository{byHash: map[string]fakeKeyRow{
		hash: {apiKey: apiKey, installation: installation},
	}}
	svc := auth.NewService(fakeInstallationRepository{}, repo, nil, nil, auth.NoOpAPIKeyCache{}, nil, func() time.Time { return time.Now() })

	engine := gin.New()
	engine.Use(middleware.WithAuth(svc, false))
	engine.GET("/probe", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Authorization", "Bearer "+routerToken)
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
}

func TestWithAuthInfraFailureIsRetryable503NotInvalidKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const routerToken = "rk_valid_but_db_is_down"
	hash, _, _ := auth.APITokenFingerprint(routerToken)
	repo := &fakeAPIKeyRepository{
		byHash:    map[string]fakeKeyRow{hash: {apiKey: &auth.APIKey{ID: "key-1"}, installation: &auth.Installation{ID: "inst-1"}}},
		lookupErr: context.DeadlineExceeded,
	}
	svc := auth.NewService(fakeInstallationRepository{}, repo, nil, nil, auth.NoOpAPIKeyCache{}, nil, func() time.Time { return time.Now() })

	engine := gin.New()
	engine.Use(middleware.WithAuth(svc, false))
	engine.GET("/probe", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set(middleware.RouterKeyHeader, routerToken)
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)

	require.Equal(t, http.StatusServiceUnavailable, rr.Code,
		"an infra failure reported as 401 makes a transient outage indistinguishable from a wrong key")
	assert.Equal(t, "1", rr.Header().Get("Retry-After"))
	assert.NotContains(t, rr.Body.String(), "invalid_key")
}

func TestWithAuthUnknownKeyStays401(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &fakeAPIKeyRepository{byHash: map[string]fakeKeyRow{}}
	svc := auth.NewService(fakeInstallationRepository{}, repo, nil, nil, auth.NoOpAPIKeyCache{}, nil, func() time.Time { return time.Now() })

	engine := gin.New()
	engine.Use(middleware.WithAuth(svc, false))
	engine.GET("/probe", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set(middleware.RouterKeyHeader, "rk_no_such_key")
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)

	require.Equal(t, http.StatusUnauthorized, rr.Code)
	assert.Contains(t, rr.Body.String(), "invalid_key")
	assert.Empty(t, rr.Header().Get("Retry-After"))
}

func TestWithAuthMalformedPrefixStays401(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &fakeAPIKeyRepository{byHash: map[string]fakeKeyRow{}}
	svc := auth.NewService(fakeInstallationRepository{}, repo, nil, nil, auth.NoOpAPIKeyCache{}, nil, func() time.Time { return time.Now() })

	engine := gin.New()
	engine.Use(middleware.WithAuth(svc, false))
	engine.GET("/probe", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Authorization", "Bearer sk-ant-oat-not-a-router-key")
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)

	require.Equal(t, http.StatusUnauthorized, rr.Code)
	assert.Contains(t, rr.Body.String(), "invalid_key")
}
