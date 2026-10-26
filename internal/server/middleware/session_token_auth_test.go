package middleware_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/server/middleware"

	"github.com/gin-gonic/gin"
	"github.com/go-jose/go-jose/v4"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWithAuth_SessionTokenAuthenticatesAsIssuerWithDelegatedSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	now := time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC)
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	publicJWK, err := jose.JSONWebKey{Key: publicKey}.MarshalJSON()
	require.NoError(t, err)
	_, kid, _, err := auth.ParseSessionIssuerJWK(publicJWK)
	require.NoError(t, err)
	installation := &auth.Installation{ID: "inst-issuer", ExternalID: "org-issuer"}
	issuer := &auth.APIKey{ID: "issuer-1", InstallationID: installation.ID, KeyHash: auth.SessionIssuerKeyHash(kid), Scope: auth.ScopeSessionIssuer, IssuerPublicKey: publicKey}
	repo := &fakeAPIKeyRepository{byHash: map[string]fakeKeyRow{issuer.KeyHash: {apiKey: issuer, installation: installation}}}
	svc := auth.NewService(fakeInstallationRepository{}, repo, nil, nil, auth.NoOpAPIKeyCache{}, nil, func() time.Time { return now }).WithSessionTokens(true)

	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, auth.SessionClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: auth.SessionTokenIssuer, Audience: jwt.ClaimStrings{auth.SessionTokenAudience}, Subject: "session-7",
			IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
		},
		Task: "task-3",
	})
	token.Header["typ"], token.Header["kid"] = auth.SessionTokenType, kid
	signed, err := token.SignedString(privateKey)
	require.NoError(t, err)

	engine := gin.New()
	engine.Use(middleware.WithAuth(svc, false))
	engine.GET("/probe", func(c *gin.Context) {
		ctx := c.Request.Context()
		assert.Equal(t, "issuer-1", ctx.Value(proxy.APIKeyIDContextKey{}), "spend and attribution stay on the issuer key")
		session := auth.DelegatedSessionFrom(ctx)
		require.NotNil(t, session)
		assert.Equal(t, "session-7", session.SessionID)
		assert.Equal(t, "task-3", session.TaskID)
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Authorization", "Bearer rs_"+signed)
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	svc.WithSessionTokens(false)
	rr = httptest.NewRecorder()
	engine.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}
