package middleware_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"weave-os/router/internal/api/subscriptions"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/server/middleware"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestSubscriptionAccountListReportsLiveIdentityLookupFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const routerToken = "rk_subscription_owner_test"
	hash, prefix, suffix := auth.APITokenFingerprint(routerToken)
	apiKey := &auth.APIKey{
		ID:                  "subscription-key",
		InstallationID:      "subscription-installation",
		CredentialSubjectID: "subscription-subject",
		KeyHash:             hash,
		KeyPrefix:           prefix,
		KeySuffix:           suffix,
	}
	installation := &auth.Installation{ID: apiKey.InstallationID, ExternalID: "subscription-org"}
	apiKeys := &fakeAPIKeyRepository{byHash: map[string]fakeKeyRow{
		hash: {apiKey: apiKey, installation: installation},
	}}
	authService := auth.NewService(fakeInstallationRepository{}, apiKeys, nil, nil, auth.NoOpAPIKeyCache{}, nil, time.Now).
		WithCredentialSubjectLookup(failingCredentialSubjectLookup{err: errors.New("identity database unavailable")}).
		WithSubscriptionAccounts(failingSubscriptionAccountRepository{err: errors.New("subscription database unavailable")})

	engine := gin.New()
	group := engine.Group("/v1")
	group.Use(middleware.WithAuth(authService, false))
	subscriptions.Register(group, authService)
	request := httptest.NewRequest(http.MethodGet, "/v1/subscriptions/accounts", nil)
	request.Header.Set(middleware.RouterKeyHeader, routerToken)
	response := httptest.NewRecorder()

	engine.ServeHTTP(response, request)

	require.Equal(t, http.StatusServiceUnavailable, response.Code)
	require.Contains(t, response.Body.String(), "subscription_owner_unavailable")
}
