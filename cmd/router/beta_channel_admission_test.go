package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/postgres"
	servingpostgres "weave-os/router/internal/postgres/serving"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/server"
	"weave-os/router/internal/server/middleware"
	"weave-os/router/smoke/hmmstub"
)

const (
	betaChainStableURL = "https://stable.example.test"
	betaChainBetaURL   = "https://beta.example.test"
	betaChainBody      = `{"model":"weave-auto-beta","messages":[{"role":"user","content":"hello"}]}`
)

type betaChainFixture struct {
	pool           *pgxpool.Pool
	authService    *auth.Service
	installationID uuid.UUID
	token          string
}

func betaChainDatabase(t *testing.T) betaChainFixture {
	t.Helper()
	dsn := os.Getenv("ROUTER_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ROUTER_TEST_DATABASE_URL not set; skipping database-backed admission chain test")
	}
	parsed, err := url.Parse(dsn)
	require.NoError(t, err)
	require.Contains(t, []string{"localhost", "127.0.0.1", "::1"}, parsed.Hostname(), "the admission chain test writes fixture rows and must target a loopback database")
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	repositories := postgres.NewRepository(pool, auth.NoOpEncryptor{})
	installation, err := repositories.Installations.Create(ctx, auth.CreateInstallationParams{ExternalID: "beta_chain_" + uuid.NewString()[:16], Name: "Beta channel admission fixture"})
	require.NoError(t, err)
	installationID := uuid.MustParse(installation.ID)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM router.model_router_installations WHERE id = $1`, installationID)
	})
	token := auth.GenerateID(auth.APIKeyPrefix)
	hash, prefix, suffix := auth.APITokenFingerprint(token)
	_, err = repositories.APIKeys.Create(ctx, auth.CreateAPIKeyParams{InstallationID: installation.ID, ExternalID: auth.GenerateID("kid"), KeyHash: hash, KeyPrefix: prefix, KeySuffix: suffix, Scope: auth.ScopeRouting})
	require.NoError(t, err)
	authService := auth.NewService(repositories.Installations, repositories.APIKeys, repositories.ExternalAPIKeys, repositories.Users, auth.NoOpAPIKeyCache{}, nil, time.Now).
		WithEncryptor(auth.NoOpEncryptor{}).
		WithCredentialSubjectLookup(postgres.NewCredentialSubjectRepo(pool)).
		WithRoutingPolicies(repositories.RoutingPolicies, nil)
	return betaChainFixture{pool: pool, authService: authService, installationID: installationID, token: token}
}

func (f betaChainFixture) grantBeta(t *testing.T) {
	t.Helper()
	_, err := f.pool.Exec(context.Background(), `INSERT INTO router.installation_channel_permissions (installation_id, channel) VALUES ($1, 'beta')`, f.installationID)
	require.NoError(t, err)
}

func (f betaChainFixture) sessionBindings(t *testing.T) int {
	t.Helper()
	var count int
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT count(*) FROM router.session_release_bindings WHERE installation_id = $1`, f.installationID).Scan(&count))
	return count
}

func betaChainWorker(t *testing.T, fixture betaChainFixture, target policyregistry.ServingTarget) *gin.Engine {
	t.Helper()
	classifier := httptest.NewServer(hmmstub.Handler())
	t.Cleanup(classifier.Close)
	graph, err := hmmstub.NewServingGraphForTarget(classifier.URL, target)
	require.NoError(t, err)
	registry := t.TempDir()
	require.NoError(t, graph.Write(registry))
	configuration := hmmstub.ServingConfiguration()
	for name, value := range map[string]string{
		"ROUTER_SERVING_TARGET":                   string(target),
		"ROUTER_SERVING_PROJECT":                  hmmstub.ServingProject,
		"ROUTER_SERVING_REGION":                   hmmstub.ServingRegion,
		"ROUTER_SERVING_REVISION":                 hmmstub.ServingRevision,
		"ROUTER_SERVING_IMAGE_DIGEST":             hmmstub.ServingImageDigest,
		"ROUTER_SERVING_REGISTRY_URI":             registry,
		"ROUTER_SERVING_CONFIGURATION_URI":        configuration.URI,
		"ROUTER_SERVING_CONFIGURATION_SHA256":     configuration.SHA256,
		"ROUTER_SERVING_CONFIGURATION_GENERATION": strconv.FormatInt(configuration.Generation, 10),
		"ROUTER_SERVING_SELECTION_SET_URI":        graph.SelectionSet.URI,
		"ROUTER_SERVING_SELECTION_SET_SHA256":     graph.SelectionSet.SHA256,
		"ROUTER_SERVING_SELECTION_SET_GENERATION": strconv.FormatInt(graph.SelectionSet.Generation, 10),
		"ROUTER_HMM_SIDECAR_AUTH":                 policySidecarAuthNone,
		"ROUTER_STABLE_BASE_URL":                  betaChainStableURL,
		"ROUTER_BETA_BASE_URL":                    betaChainBetaURL,
	} {
		t.Setenv(name, value)
	}
	admission, _, closeRegistry, err := buildManagedServingRuntime(context.Background(), server.DeploymentModeSelfHosted, nil)
	require.NoError(t, err)
	t.Cleanup(closeRegistry)
	admission.Decisions, err = buildAdmissionDecisions(fixture.pool)
	require.NoError(t, err)
	admission.Attribution = servingpostgres.NewRequestAttributionRepo(fixture.pool)
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/v1/messages",
		observability.Middleware(),
		middleware.WithAuth(fixture.authService, false, admission),
		middleware.WithServingAdmission(admission),
		middleware.WithClassifierThread(betaChainThreads{}, func(router.Strategy) bool { return true }),
		func(c *gin.Context) {
			identity, _ := requestcontext.ServingIdentityFromContext(c.Request.Context())
			c.JSON(http.StatusOK, gin.H{"serving_target": identity.Target, "routing_model": string(router.StrategySelectionFromContext(c.Request.Context()).RoutingModel)})
		},
	)
	return engine
}

type betaChainThreads struct{}

func (betaChainThreads) AdmitClassifierThread(ctx context.Context, _ string) (context.Context, error) {
	return ctx, nil
}

func sendBetaChainRequest(t *testing.T, engine *gin.Engine, token string) (int, map[string]string) {
	t.Helper()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/messages", strings.NewReader(betaChainBody))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Session-Id", uuid.NewString())
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	body := map[string]string{}
	_ = json.Unmarshal(response.Body.Bytes(), &body)
	return response.Code, body
}

func TestBetaRoutingModelAdmitsOnBetaWorkerWithPermission(t *testing.T) {
	fixture := betaChainDatabase(t)
	fixture.grantBeta(t)
	engine := betaChainWorker(t, fixture, policyregistry.TargetBeta)
	status, body := sendBetaChainRequest(t, engine, fixture.token)
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, string(policyregistry.TargetBeta), body["serving_target"])
	assert.Equal(t, string(router.ModelAutomatic), body["routing_model"], "weave-auto-beta serves as weave-auto on the beta channel")
	assert.Equal(t, 1, fixture.sessionBindings(t))
}

func TestBetaRoutingModelRefusedOnBetaWorkerWithoutPermission(t *testing.T) {
	fixture := betaChainDatabase(t)
	engine := betaChainWorker(t, fixture, policyregistry.TargetBeta)
	status, body := sendBetaChainRequest(t, engine, fixture.token)
	require.Equal(t, http.StatusForbidden, status, body)
	assert.Equal(t, "serving_channel_not_permitted", body["error"])
	assert.Equal(t, "beta", body["channel"])
	assert.Equal(t, betaChainStableURL, body["stable_base_url"])
	assert.Zero(t, fixture.sessionBindings(t), "a refused admission must not write a session binding")
}

func TestBetaRoutingModelMisdirectedOffTheBetaWorker(t *testing.T) {
	for _, target := range []policyregistry.ServingTarget{policyregistry.TargetStable, policyregistry.TargetInternal} {
		t.Run(string(target), func(t *testing.T) {
			fixture := betaChainDatabase(t)
			fixture.grantBeta(t)
			engine := betaChainWorker(t, fixture, target)
			status, body := sendBetaChainRequest(t, engine, fixture.token)
			require.Equal(t, http.StatusMisdirectedRequest, status, body)
			assert.Equal(t, "misdirected_serving_channel", body["error"])
			assert.Equal(t, betaChainBetaURL, body["base_url"])
			assert.Zero(t, fixture.sessionBindings(t), "a misdirected request must not write a session binding")
		})
	}
}
