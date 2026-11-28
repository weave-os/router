package auth_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"testing"
	"time"

	"weave-os/router/internal/auth"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	gatewayIssuerURL = "https://issuer.gateway.example"
	gatewayAudience  = "router-inference"
	gatewaySubject   = "gateway-workload"
	gatewayKID       = "gateway-key-1"
)

type fakeOIDCKeySet map[string]crypto.PublicKey

func (f fakeOIDCKeySet) VerificationKey(_ context.Context, issuerURL, kid string) (crypto.PublicKey, error) {
	if key, ok := f[issuerURL+"#"+kid]; ok {
		return key, nil
	}
	return nil, auth.ErrInvalidToken
}

type gatewayFixture struct {
	issuer  sessionIssuerFixture
	signing *rsa.PrivateKey
	config  auth.UpstreamGatewayIssuer
}

func newGatewayFixture(t *testing.T) gatewayFixture {
	t.Helper()
	signing, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	issuer := newSessionIssuerFixture(t)
	return gatewayFixture{
		issuer:  issuer,
		signing: signing,
		config: auth.UpstreamGatewayIssuer{
			IssuerURL:        gatewayIssuerURL,
			Audience:         gatewayAudience,
			Subjects:         []string{gatewaySubject},
			Algorithms:       []string{"RS256"},
			SessionIssuerKID: issuer.kid,
			SessionHeaders:   []string{"X-Gateway-Session"},
		},
	}
}

func (f gatewayFixture) service(t *testing.T) *auth.Service {
	t.Helper()
	svc, _ := f.issuer.service(t)
	return svc.WithUpstreamGatewayIssuers([]auth.UpstreamGatewayIssuer{f.config},
		fakeOIDCKeySet{gatewayIssuerURL + "#" + gatewayKID: &f.signing.PublicKey})
}

func (f gatewayFixture) claims() jwt.RegisteredClaims {
	return jwt.RegisteredClaims{
		Issuer:    gatewayIssuerURL,
		Audience:  jwt.ClaimStrings{gatewayAudience},
		Subject:   gatewaySubject,
		IssuedAt:  jwt.NewNumericDate(sessionClock.Add(-time.Minute)),
		ExpiresAt: jwt.NewNumericDate(sessionClock.Add(10 * time.Minute)),
	}
}

func (f gatewayFixture) sign(t *testing.T, claims jwt.RegisteredClaims, mutate func(*jwt.Token)) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = gatewayKID
	if mutate != nil {
		mutate(token)
	}
	signed, err := token.SignedString(f.signing)
	require.NoError(t, err)
	return signed
}

func TestVerifyUpstreamGatewayToken_AuthenticatesAsTheBoundIssuer(t *testing.T) {
	f := newGatewayFixture(t)
	token := f.sign(t, f.claims(), nil)
	require.True(t, auth.IsUpstreamGatewayToken(token))

	inst, key, _, _, principal, err := f.service(t).VerifyUpstreamGatewayToken(context.Background(), token)

	require.NoError(t, err)
	assert.Equal(t, f.issuer.inst, inst)
	assert.Equal(t, f.issuer.apiKey.ID, key.ID)
	require.NotNil(t, principal)
	assert.Equal(t, gatewayIssuerURL, principal.IssuerURL)
	assert.Equal(t, gatewaySubject, principal.Subject)
	assert.Equal(t, []string{"X-Gateway-Session"}, principal.SessionHeaders)
	assert.Empty(t, principal.SessionID, "an OIDC gateway token names a workload, not a session")
}

func TestUpstreamGatewayForSessionToken_FollowsTheBoundIssuer(t *testing.T) {
	f := newGatewayFixture(t)
	svc := f.service(t)
	_, key, _, _, session, err := svc.VerifySessionToken(context.Background(), f.issuer.sign(t, f.issuer.claims(), nil), true)
	require.NoError(t, err)

	principal := svc.UpstreamGatewayForSessionToken(key, session)
	require.NotNil(t, principal, "an rs_ token from a gateway-bound issuer runs in gateway mode")
	assert.Equal(t, gatewayIssuerURL, principal.IssuerURL)
	assert.Equal(t, "session-1", principal.SessionID, "the signed sub is the gateway session")

	unbound, _ := f.issuer.service(t)
	assert.Nil(t, unbound.UpstreamGatewayForSessionToken(key, session), "an issuer no gateway is bound to stays on the plain session path")
}

func TestVerifyUpstreamGatewayToken_RejectsInvalidTokens(t *testing.T) {
	f := newGatewayFixture(t)
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	with := func(mutate func(*jwt.RegisteredClaims)) string {
		claims := f.claims()
		mutate(&claims)
		return f.sign(t, claims, nil)
	}
	cases := map[string]string{
		"wrong audience":      with(func(c *jwt.RegisteredClaims) { c.Audience = jwt.ClaimStrings{"weave-router"} }),
		"unconfigured issuer": with(func(c *jwt.RegisteredClaims) { c.Issuer = "https://other.example" }),
		"subject not allowed": with(func(c *jwt.RegisteredClaims) { c.Subject = "someone-else" }),
		"expired":             with(func(c *jwt.RegisteredClaims) { c.ExpiresAt = jwt.NewNumericDate(sessionClock.Add(-time.Minute)) }),
		"missing expiry":      with(func(c *jwt.RegisteredClaims) { c.ExpiresAt = nil }),
		"missing issued at":   with(func(c *jwt.RegisteredClaims) { c.IssuedAt = nil }),
		"lifetime over limit": with(func(c *jwt.RegisteredClaims) { c.ExpiresAt = jwt.NewNumericDate(sessionClock.Add(2 * time.Hour)) }),
		"unknown kid":         f.sign(t, f.claims(), func(tok *jwt.Token) { tok.Header["kid"] = "rotated-away" }),
		"missing kid":         f.sign(t, f.claims(), func(tok *jwt.Token) { delete(tok.Header, "kid") }),
		"garbage":             "eyJhbGciOiJSUzI1NiJ9.e30.c2ln",
		"signed by other key": func() string {
			token := jwt.NewWithClaims(jwt.SigningMethodRS256, f.claims())
			token.Header["kid"] = gatewayKID
			signed, signErr := token.SignedString(other)
			require.NoError(t, signErr)
			return signed
		}(),
		"symmetric algorithm": func() string {
			token := jwt.NewWithClaims(jwt.SigningMethodHS256, f.claims())
			token.Header["kid"] = gatewayKID
			signed, signErr := token.SignedString([]byte("shared-secret"))
			require.NoError(t, signErr)
			return signed
		}(),
	}
	svc := f.service(t)
	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, _, _, principal, err := svc.VerifyUpstreamGatewayToken(context.Background(), token)
			require.ErrorIs(t, err, auth.ErrInvalidToken)
			assert.Nil(t, principal)
		})
	}
}

func TestVerifyUpstreamGatewayToken_RevokedIssuerRejectsTheGateway(t *testing.T) {
	f := newGatewayFixture(t)
	svc, repo := f.issuer.service(t)
	delete(repo.byHash, f.issuer.apiKey.KeyHash)
	svc = svc.WithUpstreamGatewayIssuers([]auth.UpstreamGatewayIssuer{f.config},
		fakeOIDCKeySet{gatewayIssuerURL + "#" + gatewayKID: &f.signing.PublicKey})

	_, _, _, _, _, err := svc.VerifyUpstreamGatewayToken(context.Background(), f.sign(t, f.claims(), nil))
	require.ErrorIs(t, err, auth.ErrInvalidToken)
}

func TestVerifyUpstreamGatewayToken_DisabledWithoutConfiguration(t *testing.T) {
	f := newGatewayFixture(t)
	svc, _ := f.issuer.service(t)
	_, _, _, _, _, err := svc.VerifyUpstreamGatewayToken(context.Background(), f.sign(t, f.claims(), nil))
	require.ErrorIs(t, err, auth.ErrInvalidToken)
}

func TestIsUpstreamGatewayToken_LeavesRouterCredentialsAlone(t *testing.T) {
	for _, token := range []string{"", "rk_abc", "rs_eyJa.b.c", "eyJhbGciOiJSUzI1NiJ9", "not.a.jwt"} {
		assert.False(t, auth.IsUpstreamGatewayToken(token), token)
	}
}

func TestParseUpstreamGatewayIssuers(t *testing.T) {
	valid := `[{"issuer_url":"https://issuer.gateway.example","audience":"router-inference","subjects":["gateway-workload"],"session_issuer_kid":"kid-1","session_headers":["X-Gateway-Session"]}]`
	issuers, err := auth.ParseUpstreamGatewayIssuers(valid)
	require.NoError(t, err)
	require.Len(t, issuers, 1)
	assert.Equal(t, []string{"X-Gateway-Session"}, issuers[0].SessionHeaders)
	assert.Equal(t, auth.DefaultUpstreamGatewayAlgorithms, issuers[0].Algorithms)
	assert.Equal(t, auth.DefaultUpstreamGatewayMaxLifetime, issuers[0].MaxLifetime)

	empty, err := auth.ParseUpstreamGatewayIssuers("  ")
	require.NoError(t, err)
	assert.Empty(t, empty)

	for name, raw := range map[string]string{
		"not json":            `{`,
		"plain http issuer":   `[{"issuer_url":"http://issuer.example","audience":"a","subjects":["s"],"session_issuer_kid":"k"}]`,
		"missing audience":    `[{"issuer_url":"https://issuer.example","subjects":["s"],"session_issuer_kid":"k"}]`,
		"missing subjects":    `[{"issuer_url":"https://issuer.example","audience":"a","session_issuer_kid":"k"}]`,
		"missing issuer kid":  `[{"issuer_url":"https://issuer.example","audience":"a","subjects":["s"]}]`,
		"symmetric algorithm": `[{"issuer_url":"https://issuer.example","audience":"a","subjects":["s"],"session_issuer_kid":"k","algorithms":["HS256"]}]`,
		"none algorithm":      `[{"issuer_url":"https://issuer.example","audience":"a","subjects":["s"],"session_issuer_kid":"k","algorithms":["none"]}]`,
		"bad session header":  `[{"issuer_url":"https://issuer.example","audience":"a","subjects":["s"],"session_issuer_kid":"k","session_headers":["bad header"]}]`,
		"user email header":   `[{"issuer_url":"https://issuer.example","audience":"a","subjects":["s"],"session_issuer_kid":"k","user_email_headers":["X-Gateway-User-Email"]}]`,
		"duplicate issuer":    `[{"issuer_url":"https://issuer.example","audience":"a","subjects":["s"],"session_issuer_kid":"k"},{"issuer_url":"https://issuer.example","audience":"b","subjects":["s"],"session_issuer_kid":"k"}]`,
		"bad max lifetime":    `[{"issuer_url":"https://issuer.example","audience":"a","subjects":["s"],"session_issuer_kid":"k","max_lifetime":"forever"}]`,
		"trailing value":      `[{"issuer_url":"https://issuer.example","audience":"a","subjects":["s"],"session_issuer_kid":"k"}] [{"issuer_url":"https://other.example"}]`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := auth.ParseUpstreamGatewayIssuers(raw)
			require.ErrorIs(t, err, auth.ErrInvalidUpstreamGatewayIssuer)
		})
	}
}
