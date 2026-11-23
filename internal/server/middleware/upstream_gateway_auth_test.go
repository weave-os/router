package middleware_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/server/middleware"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type staticOIDCKeys struct{ key crypto.PublicKey }

func (s staticOIDCKeys) VerificationKey(context.Context, string, string) (crypto.PublicKey, error) {
	return s.key, nil
}

type gatewayAuthHarness struct {
	engine *gin.Engine
	token  string
	seen   *http.Request
}

func newGatewayAuthHarness(t *testing.T) *gatewayAuthHarness {
	t.Helper()
	gin.SetMode(gin.TestMode)
	now := time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC)
	signing, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	installation := &auth.Installation{ID: "inst-gateway", ExternalID: "org-gateway"}
	issuer := &auth.APIKey{ID: "issuer-gw", InstallationID: installation.ID, KeyHash: auth.SessionIssuerKeyHash("issuer-kid"), Scope: auth.ScopeSessionIssuer, IssuerPublicKey: make([]byte, 32)}
	repo := &fakeAPIKeyRepository{byHash: map[string]fakeKeyRow{issuer.KeyHash: {apiKey: issuer, installation: installation}}}
	svc := auth.NewService(fakeInstallationRepository{}, repo, nil, nil, auth.NoOpAPIKeyCache{}, nil, func() time.Time { return now }).
		WithUpstreamGatewayIssuers([]auth.UpstreamGatewayIssuer{{
			IssuerURL: "https://issuer.gateway.example", Audience: "router-inference", Subjects: []string{"gateway-workload"},
			SessionIssuerKID: "issuer-kid", SessionHeaders: []string{"X-Gateway-Session"},
			UserEmailHeaders: []string{"X-Gateway-User-Email"}, UserNameHeaders: []string{"X-Gateway-User-Name"},
		}}, staticOIDCKeys{key: &signing.PublicKey})

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.RegisteredClaims{
		Issuer: "https://issuer.gateway.example", Audience: jwt.ClaimStrings{"router-inference"}, Subject: "gateway-workload",
		IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(5 * time.Minute)),
	})
	token.Header["kid"] = "gateway-key-1"
	signed, err := token.SignedString(signing)
	require.NoError(t, err)

	h := &gatewayAuthHarness{engine: gin.New(), token: signed}
	h.engine.Use(middleware.WithAuth(svc, false))
	probe := func(c *gin.Context) {
		h.seen = c.Request
		c.Status(http.StatusOK)
	}
	h.engine.POST("/v1/messages", probe)
	h.engine.POST("/v1/chat/completions", probe)
	return h
}

func (h *gatewayAuthHarness) serve(t *testing.T, path string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, nil)
	req.Header.Set("Authorization", "Bearer "+h.token)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	h.engine.ServeHTTP(rr, req)
	return rr
}

func refusalCode(t *testing.T, rr *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body), rr.Body.String())
	return body.Error.Code
}

func TestWithAuth_UpstreamGatewayTokenEntersGatewayMode(t *testing.T) {
	h := newGatewayAuthHarness(t)
	rr := h.serve(t, "/v1/messages", map[string]string{
		"X-Gateway-Session":    "gw-session-1",
		"X-Gateway-User-Email": "gateway.user@example.com",
		"X-Gateway-User-Name":  "Gateway User",
	})
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	ctx := h.seen.Context()
	assert.Equal(t, "issuer-gw", ctx.Value(requestcontext.APIKeyIDContextKey{}), "spend and attribution stay on the bound issuer key")
	upstreamGateway, ok := requestcontext.UpstreamGatewayFrom(ctx)
	require.True(t, ok)
	assert.Equal(t, "gw-session-1", upstreamGateway.SessionID)
	assert.Equal(t, "gateway.user@example.com", upstreamGateway.Email)
	assert.Equal(t, "Gateway User", upstreamGateway.DisplayName)
	assert.Empty(t, h.seen.Header.Get("Authorization"), "the inbound gateway credential must not stay on the request")
}

func TestWithAuth_UpstreamGatewayTokenOnlyServesMessages(t *testing.T) {
	h := newGatewayAuthHarness(t)
	rr := h.serve(t, "/v1/chat/completions", nil)
	require.Equal(t, http.StatusForbidden, rr.Code)
	assert.Equal(t, "upstream_gateway_surface_rejected", refusalCode(t, rr))
	assert.Nil(t, h.seen)
}

func TestWithAuth_UpstreamGatewayRejectsTheRoutersOwnInnerCall(t *testing.T) {
	h := newGatewayAuthHarness(t)
	rr := h.serve(t, "/v1/messages", map[string]string{requestcontext.UpstreamGatewayHopHeader: "1"})
	require.Equal(t, http.StatusForbidden, rr.Code)
	assert.Equal(t, "upstream_gateway_loop_detected", refusalCode(t, rr))
	assert.Nil(t, h.seen)
}
