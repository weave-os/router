package middleware_test

import (
	"context"
	"crypto"
	"crypto/ed25519"
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
	"github.com/go-jose/go-jose/v4"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type staticOIDCKeys struct{ key crypto.PublicKey }

func (s staticOIDCKeys) VerificationKey(context.Context, string, string) (crypto.PublicKey, error) {
	return s.key, nil
}

type gatewayAuthHarness struct {
	engine       *gin.Engine
	token        string
	sessionToken string
	seen         *http.Request
}

func newGatewayAuthHarness(t *testing.T) *gatewayAuthHarness {
	t.Helper()
	gin.SetMode(gin.TestMode)
	now := time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC)
	signing, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	issuerPublic, issuerPrivate, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	issuerJWK, err := jose.JSONWebKey{Key: issuerPublic}.MarshalJSON()
	require.NoError(t, err)
	_, issuerKID, _, err := auth.ParseSessionIssuerJWK(issuerJWK)
	require.NoError(t, err)
	installation := &auth.Installation{ID: "inst-gateway", ExternalID: "org-gateway"}
	issuer := &auth.APIKey{ID: "issuer-gw", InstallationID: installation.ID, KeyHash: auth.SessionIssuerKeyHash(issuerKID), Scope: auth.ScopeSessionIssuer, IssuerPublicKey: issuerPublic}
	repo := &fakeAPIKeyRepository{byHash: map[string]fakeKeyRow{issuer.KeyHash: {apiKey: issuer, installation: installation}}}
	svc := auth.NewService(fakeInstallationRepository{}, repo, nil, nil, auth.NoOpAPIKeyCache{}, nil, func() time.Time { return now }).
		WithUpstreamGatewayIssuers([]auth.UpstreamGatewayIssuer{{
			IssuerURL: "https://issuer.gateway.example", Audience: "router-inference", Subjects: []string{"gateway-workload"},
			SessionIssuerKID: issuerKID, SessionHeaders: []string{"X-Gateway-Session"},
		}}, staticOIDCKeys{key: &signing.PublicKey})

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.RegisteredClaims{
		Issuer: "https://issuer.gateway.example", Audience: jwt.ClaimStrings{"router-inference"}, Subject: "gateway-workload",
		IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(5 * time.Minute)),
	})
	token.Header["kid"] = "gateway-key-1"
	signed, err := token.SignedString(signing)
	require.NoError(t, err)

	session := jwt.NewWithClaims(jwt.SigningMethodEdDSA, auth.SessionClaims{
		IssuerID: issuer.ID,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: auth.SessionTokenIssuer, Audience: jwt.ClaimStrings{auth.SessionTokenAudience}, Subject: "signed-session-1",
			IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
		},
	})
	session.Header["typ"], session.Header["kid"] = auth.SessionTokenType, issuerKID
	signedSession, err := session.SignedString(issuerPrivate)
	require.NoError(t, err)

	h := &gatewayAuthHarness{engine: gin.New(), token: signed, sessionToken: auth.SessionTokenPrefix + "_" + signedSession}
	h.engine.Use(middleware.WithAuth(svc, false))
	probe := func(c *gin.Context) {
		h.seen = c.Request
		c.Status(http.StatusOK)
	}
	h.engine.POST("/v1/messages", probe)
	h.engine.POST("/v1/messages/count_tokens", probe)
	h.engine.POST("/v1/chat/completions", probe)
	return h
}

func (h *gatewayAuthHarness) serve(t *testing.T, path string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	return h.serveWith(t, h.token, path, headers)
}

func (h *gatewayAuthHarness) serveWith(t *testing.T, token, path string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	h.seen = nil
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
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
		"X-Gateway-Session":  "gw-session-1",
		"X-Weave-User-Email": "engineer@example.com",
		"X-Weave-User-Name":  "Engineer",
	})
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	ctx := h.seen.Context()
	assert.Equal(t, "issuer-gw", ctx.Value(requestcontext.APIKeyIDContextKey{}), "spend and attribution stay on the bound issuer key")
	upstreamGateway, ok := requestcontext.UpstreamGatewayFrom(ctx)
	require.True(t, ok)
	assert.Equal(t, "gw-session-1", upstreamGateway.SessionID)
	assert.Empty(t, h.seen.Header.Get("Authorization"), "the inbound gateway credential must not stay on the request")
	assert.Empty(t, h.seen.Header.Get("X-Weave-User-Email"), "inbound end-user email is dropped in gateway mode")
	assert.Empty(t, h.seen.Header.Get("X-Weave-User-Name"))
}

func TestWithAuth_UpstreamGatewayAllowsCountTokens(t *testing.T) {
	h := newGatewayAuthHarness(t)
	rr := h.serve(t, "/v1/messages/count_tokens", nil)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	_, ok := requestcontext.UpstreamGatewayFrom(h.seen.Context())
	assert.True(t, ok)
}

func TestWithAuth_SessionTokenFromGatewayBoundIssuerGetsGatewayProtections(t *testing.T) {
	h := newGatewayAuthHarness(t)

	rr := h.serveWith(t, h.sessionToken, "/v1/messages", map[string]string{
		"X-Gateway-Session":  "header-session",
		"X-Weave-User-Email": "engineer@example.com",
	})
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	upstreamGateway, ok := requestcontext.UpstreamGatewayFrom(h.seen.Context())
	require.True(t, ok, "an rs_ token from a gateway-bound issuer runs in gateway mode")
	assert.Equal(t, "signed-session-1", upstreamGateway.SessionID, "the signed sub is the session key, not a header")
	assert.Equal(t, "signed-session-1", requestcontext.ClientIdentityFrom(requestcontext.WithClientIdentity(h.seen.Context(), requestcontext.ClientIdentity{Email: "engineer@example.com"})).SessionID)
	assert.Empty(t, h.seen.Header.Get("Authorization"))
	assert.Empty(t, h.seen.Header.Get("X-Weave-User-Email"))

	rr = h.serveWith(t, h.sessionToken, "/v1/chat/completions", nil)
	require.Equal(t, http.StatusForbidden, rr.Code)
	assert.Equal(t, "upstream_gateway_surface_rejected", refusalCode(t, rr))
	assert.Nil(t, h.seen)

	rr = h.serveWith(t, h.sessionToken, "/v1/messages", map[string]string{requestcontext.UpstreamGatewayHopHeader: "1"})
	require.Equal(t, http.StatusForbidden, rr.Code)
	assert.Equal(t, "upstream_gateway_loop_detected", refusalCode(t, rr))
	assert.Nil(t, h.seen)
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
