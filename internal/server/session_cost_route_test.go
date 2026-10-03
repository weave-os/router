package server_test

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/server"
	"weave-os/router/internal/server/middleware"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type readKeyRepo struct {
	auth.APIKeyRepository
	installation *auth.Installation
	scopes       map[string]auth.APIKeyScope
	subjectIDs   map[string]string
}

func TestThreadHandshakeKeepsCredentialOnlyAuthWithManagedServing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	installation := &auth.Installation{ID: uuid.NewString()}
	repo := readKeyRepo{installation: installation, scopes: map[string]auth.APIKeyScope{"rk_thread": auth.ScopeRouting}}
	authSvc := auth.NewService(nil, repo, nil, nil, auth.NoOpAPIKeyCache{}, nil, time.Now)
	engine := gin.New()
	server.RegisterWithFeatures(engine, authSvc, nil, nil, nil, server.DeploymentModeManaged, nil, nil, nil, nil, server.Features{ServingAdmission: &middleware.ServingAdmissionConfig{}})
	request := httptest.NewRequest(http.MethodPost, "/v1/router/threads", strings.NewReader(`{}`))
	request.Header.Set(auth.RouterKeyHeader, "rk_thread")
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	require.Equal(t, http.StatusBadRequest, response.Code)
	require.Contains(t, response.Body.String(), "invalid_new_chat_id", "handshake must reach its own validator without a serving assertion")
}

func (r readKeyRepo) GetActiveByHashWithInstallation(_ context.Context, hash string) (*auth.APIKey, *auth.Installation, error) {
	for token, scope := range r.scopes {
		if auth.HashAPIKeySHA256(token) == hash {
			return &auth.APIKey{ID: "key-" + token, InstallationID: r.installation.ID, Scope: scope, CredentialSubjectID: r.subjectIDs[token]}, r.installation, nil
		}
	}
	return nil, nil, sql.ErrNoRows
}

func (readKeyRepo) MarkUsed(context.Context, string) (bool, error) { return false, nil }

type readSubjectRepo map[string]*auth.CredentialSubject

func (r readSubjectRepo) GetCredentialSubject(_ context.Context, subjectID, _ string) (*auth.CredentialSubject, error) {
	subject, ok := r[subjectID]
	if !ok {
		return nil, sql.ErrNoRows
	}
	return subject, nil
}

type costTelemetry struct {
	proxy.TelemetryRepository
	installationID string
	sessionID      string
}

func (c *costTelemetry) GetSessionCost(_ context.Context, installationID, sessionID string) (proxy.SessionCost, error) {
	c.installationID = installationID
	c.sessionID = sessionID
	return proxy.SessionCost{SessionID: sessionID, RequestCount: 1, LastRecordedAt: time.Now()}, nil
}

// A managed worker verifies gateway assertions on inference routes; the cost
// read must authenticate either key type without one, and ra_ must stay out of
// the inference groups.
func TestSessionCostRouteAcceptsReadKeysWithoutServingAdmission(t *testing.T) {
	gin.SetMode(gin.TestMode)
	installation := &auth.Installation{ID: uuid.NewString(), ExternalID: "ext-cost"}
	repo := readKeyRepo{installation: installation, scopes: map[string]auth.APIKeyScope{
		"rk_routing":   auth.ScopeRouting,
		"ra_analytics": auth.ScopeAnalyticsRead,
		"rk_disabled":  auth.ScopeRouting,
	}, subjectIDs: map[string]string{"rk_disabled": "subject-disabled"}}
	authSvc := auth.NewService(nil, repo, nil, nil, auth.NoOpAPIKeyCache{}, nil, time.Now).
		WithCredentialSubjectLookup(readSubjectRepo{"subject-disabled": {ID: "subject-disabled", ProjectionComplete: true, AccessEnabled: false, EnrollmentGeneration: 1}})
	telemetry := &costTelemetry{}
	proxySvc := proxy.NewService(nil, nil, nil, false, nil, nil, false, "", "", telemetry)
	engine := gin.New()
	engine.UseRawPath = true
	engine.UnescapePathValues = true
	server.RegisterWithFeatures(engine, authSvc, proxySvc, nil, nil, server.DeploymentModeManaged, nil, nil, nil, nil, server.Features{ServingAdmission: &middleware.ServingAdmissionConfig{}})

	serve := func(method, path, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)
		return rec
	}

	for _, token := range []string{"rk_routing", "ra_analytics"} {
		telemetry.installationID = ""
		rec := serve(http.MethodGet, "/v1/sessions/session-1/cost", token)
		require.Equal(t, http.StatusOK, rec.Code, token+": "+rec.Body.String())
		assert.Equal(t, installation.ID, telemetry.installationID, token)
		assert.Equal(t, "500", rec.Header().Get("X-RateLimit-Limit"), token)
	}
	encodedSlash := serve(http.MethodGet, "/v1/sessions/abc%2Fdef/cost", "rk_routing")
	require.Equal(t, http.StatusOK, encodedSlash.Code, encodedSlash.Body.String())
	assert.Equal(t, "abc/def", telemetry.sessionID)
	telemetry.installationID = "unchanged"
	assert.Equal(t, http.StatusUnauthorized, serve(http.MethodGet, "/v1/sessions/session-1/cost", "rk_disabled").Code)
	assert.Equal(t, "unchanged", telemetry.installationID, "a disabled personal subject must be rejected before telemetry lookup")

	emptyID := serve(http.MethodGet, "/v1/sessions//cost", "ra_analytics")
	assert.Equal(t, http.StatusBadRequest, emptyID.Code, "an empty id must be rejected before telemetry lookup")
	remaining, err := strconv.Atoi(serve(http.MethodGet, "/v1/sessions/session-1/cost", "ra_analytics").Header().Get("X-RateLimit-Remaining"))
	require.NoError(t, err)
	assert.Less(t, remaining, 498, "a per-key bucket would have spent only two ra_ tokens")

	assert.Equal(t, http.StatusUnauthorized, serve(http.MethodGet, "/v1/sessions/session-1/cost", "rk_revoked").Code)
	assert.Equal(t, http.StatusUnauthorized, serve(http.MethodGet, "/v1/models", "ra_analytics").Code, "an analytics key must stay off inference routes")
}
