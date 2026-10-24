package auth_test

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers/httputil"

	"github.com/go-jose/go-jose/v4"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var sessionClock = time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)

type sessionIssuerFixture struct {
	privateKey ed25519.PrivateKey
	publicJWK  []byte
	kid        string
	apiKey     *auth.APIKey
	inst       *auth.Installation
}

func newSessionIssuerFixture(t *testing.T) sessionIssuerFixture {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	publicJWK, err := jose.JSONWebKey{Key: publicKey}.MarshalJSON()
	require.NoError(t, err)
	thumbprint, err := (&jose.JSONWebKey{Key: publicKey}).Thumbprint(crypto.SHA256)
	require.NoError(t, err)
	kid := base64.RawURLEncoding.EncodeToString(thumbprint)
	inst := &auth.Installation{ID: "inst-issuer", ExternalID: "org-issuer"}
	return sessionIssuerFixture{
		privateKey: privateKey,
		publicJWK:  publicJWK,
		kid:        kid,
		inst:       inst,
		apiKey: &auth.APIKey{
			ID:              "issuer-1",
			InstallationID:  inst.ID,
			KeyHash:         auth.SessionIssuerKeyHash(kid),
			Scope:           auth.ScopeSessionIssuer,
			IssuerPublicKey: publicKey,
		},
	}
}

func (f sessionIssuerFixture) claims() *auth.SessionClaims {
	return &auth.SessionClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    auth.SessionTokenIssuer,
			Audience:  jwt.ClaimStrings{auth.SessionTokenAudience},
			Subject:   "session-1",
			IssuedAt:  jwt.NewNumericDate(sessionClock.Add(-time.Minute)),
			ExpiresAt: jwt.NewNumericDate(sessionClock.Add(time.Hour)),
			ID:        "jti-1",
		},
		Task: "task-1",
	}
}

func (f sessionIssuerFixture) sign(t *testing.T, claims *auth.SessionClaims, mutate func(*jwt.Token)) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	token.Header["typ"] = auth.SessionTokenType
	token.Header["kid"] = f.kid
	if mutate != nil {
		mutate(token)
	}
	signed, err := token.SignedString(f.privateKey)
	require.NoError(t, err)
	return auth.SessionTokenPrefix + "_" + signed
}

func (f sessionIssuerFixture) service(t *testing.T, enabled bool) (*auth.Service, *fakeAPIKeyRepository) {
	t.Helper()
	repo := &fakeAPIKeyRepository{byHash: map[string]fakeKeyRow{f.apiKey.KeyHash: {apiKey: f.apiKey, installation: f.inst}}}
	svc := auth.NewService(&fakeInstallationRepository{}, repo, nil, nil, auth.NoOpAPIKeyCache{}, nil, func() time.Time { return sessionClock }).
		WithSessionTokens(enabled)
	return svc, repo
}

func TestVerifySessionToken_AuthenticatesAsIssuer(t *testing.T) {
	f := newSessionIssuerFixture(t)
	svc, _ := f.service(t, true)

	inst, key, _, _, session, err := svc.VerifySessionToken(context.Background(), f.sign(t, f.claims(), nil), true)

	require.NoError(t, err)
	assert.Equal(t, f.inst, inst)
	assert.Equal(t, f.apiKey.ID, key.ID)
	require.NotNil(t, session)
	assert.Equal(t, auth.DelegatedSession{IssuerKeyID: "issuer-1", SessionID: "session-1", TaskID: "task-1", ExpiresAt: sessionClock.Add(time.Hour)}, *session)
}

func TestVerifySessionToken_RejectsInvalidTokens(t *testing.T) {
	f := newSessionIssuerFixture(t)
	_, otherKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	capMicros := int64(5_000_000)

	cases := map[string]func(t *testing.T) string{
		"alg none": func(t *testing.T) string {
			token := jwt.NewWithClaims(jwt.SigningMethodNone, f.claims())
			token.Header["typ"], token.Header["kid"] = auth.SessionTokenType, f.kid
			signed, err := token.SignedString(jwt.UnsafeAllowNoneSignatureType)
			require.NoError(t, err)
			return "rs_" + signed
		},
		"HS256 keyed with the public key": func(t *testing.T) string {
			token := jwt.NewWithClaims(jwt.SigningMethodHS256, f.claims())
			token.Header["typ"], token.Header["kid"] = auth.SessionTokenType, f.kid
			signed, err := token.SignedString([]byte(f.apiKey.IssuerPublicKey))
			require.NoError(t, err)
			return "rs_" + signed
		},
		"signed by another key": func(t *testing.T) string {
			token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, f.claims())
			token.Header["typ"], token.Header["kid"] = auth.SessionTokenType, f.kid
			signed, err := token.SignedString(otherKey)
			require.NoError(t, err)
			return "rs_" + signed
		},
		"wrong audience": func(t *testing.T) string {
			c := f.claims()
			c.Audience = jwt.ClaimStrings{"someone-else"}
			return f.sign(t, c, nil)
		},
		"wrong issuer": func(t *testing.T) string {
			c := f.claims()
			c.Issuer = "attacker"
			return f.sign(t, c, nil)
		},
		"wrong typ": func(t *testing.T) string {
			return f.sign(t, f.claims(), func(tok *jwt.Token) { tok.Header["typ"] = "JWT" })
		},
		"missing exp": func(t *testing.T) string {
			c := f.claims()
			c.ExpiresAt = nil
			return f.sign(t, c, nil)
		},
		"missing iat": func(t *testing.T) string {
			c := f.claims()
			c.IssuedAt = nil
			return f.sign(t, c, nil)
		},
		"expired": func(t *testing.T) string {
			c := f.claims()
			c.IssuedAt = jwt.NewNumericDate(sessionClock.Add(-2 * time.Hour))
			c.ExpiresAt = jwt.NewNumericDate(sessionClock.Add(-time.Hour))
			return f.sign(t, c, nil)
		},
		"lifetime over the maximum": func(t *testing.T) string {
			c := f.claims()
			c.ExpiresAt = jwt.NewNumericDate(c.IssuedAt.Add(auth.SessionTokenMaxLifetime + time.Minute))
			return f.sign(t, c, nil)
		},
		"missing subject": func(t *testing.T) string {
			c := f.claims()
			c.Subject = ""
			return f.sign(t, c, nil)
		},
		"subject longer than a billable ID": func(t *testing.T) string {
			c := f.claims()
			c.Subject = strings.Repeat("s", auth.SessionTokenMaxIDLength+1)
			return f.sign(t, c, nil)
		},
		"task longer than a billable ID": func(t *testing.T) string {
			c := f.claims()
			c.Task = strings.Repeat("t", auth.SessionTokenMaxIDLength+1)
			return f.sign(t, c, nil)
		},
		"unknown kid": func(t *testing.T) string {
			return f.sign(t, f.claims(), func(tok *jwt.Token) { tok.Header["kid"] = "unknown" })
		},
		"non-positive session cap": func(t *testing.T) string {
			c := f.claims()
			zero := int64(0)
			c.Caps = &auth.SessionCaps{SessionUSDMicros: &zero}
			return f.sign(t, c, nil)
		},
		"task cap without a task": func(t *testing.T) string {
			c := f.claims()
			c.Task = ""
			c.Caps = &auth.SessionCaps{TaskUSDMicros: &capMicros}
			return f.sign(t, c, nil)
		},
		"empty caps object": func(t *testing.T) string {
			c := f.claims()
			c.Caps = &auth.SessionCaps{}
			return f.sign(t, c, nil)
		},
		"caps with only unknown fields": func(t *testing.T) string {
			unknownCapsClaims := jwt.MapClaims{
				"iss": auth.SessionTokenIssuer, "aud": auth.SessionTokenAudience, "sub": "session-1",
				"iat": sessionClock.Add(-time.Minute).Unix(), "exp": sessionClock.Add(time.Hour).Unix(),
				"https://weave.ai/caps": map[string]int64{"session_usd_micro": 5_000_000},
			}
			token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, unknownCapsClaims)
			token.Header["typ"], token.Header["kid"] = auth.SessionTokenType, f.kid
			signed, err := token.SignedString(f.privateKey)
			require.NoError(t, err)
			return "rs_" + signed
		},
		"oversized": func(t *testing.T) string {
			return "rs_" + strings.Repeat("a", auth.SessionTokenMaxBytes)
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			svc, _ := f.service(t, true)
			_, _, _, _, _, err := svc.VerifySessionToken(context.Background(), build(t), true)
			require.ErrorIs(t, err, auth.ErrInvalidToken)
		})
	}
}

func TestVerifySessionToken_CarriesCaps(t *testing.T) {
	f := newSessionIssuerFixture(t)
	svc, _ := f.service(t, true)
	sessionCap, taskCap := int64(2_000_000), int64(9_000_000)
	c := f.claims()
	c.Caps = &auth.SessionCaps{SessionUSDMicros: &sessionCap, TaskUSDMicros: &taskCap}

	_, _, _, _, session, err := svc.VerifySessionToken(context.Background(), f.sign(t, c, nil), true)

	require.NoError(t, err)
	require.NotNil(t, session.SessionCapMicros)
	require.NotNil(t, session.TaskCapMicros)
	assert.Equal(t, sessionCap, *session.SessionCapMicros)
	assert.Equal(t, taskCap, *session.TaskCapMicros)
}

func TestVerifySessionToken_DisabledRejectsValidToken(t *testing.T) {
	f := newSessionIssuerFixture(t)
	svc, _ := f.service(t, false)

	_, _, _, _, _, err := svc.VerifySessionToken(context.Background(), f.sign(t, f.claims(), nil), true)

	require.ErrorIs(t, err, auth.ErrInvalidToken)
}

func TestVerifySessionToken_RevokedIssuerRejectsOutstandingTokens(t *testing.T) {
	f := newSessionIssuerFixture(t)
	svc, repo := f.service(t, true)
	token := f.sign(t, f.claims(), nil)
	_, _, _, _, _, err := svc.VerifySessionToken(context.Background(), token, true)
	require.NoError(t, err)

	delete(repo.byHash, f.apiKey.KeyHash)

	_, _, _, _, _, err = svc.VerifySessionToken(context.Background(), token, true)
	require.ErrorIs(t, err, auth.ErrInvalidToken)
}

func TestVerifySessionToken_RoutingKeyStoredUnderKidIsNotAnIssuer(t *testing.T) {
	f := newSessionIssuerFixture(t)
	f.apiKey.Scope = auth.ScopeRouting
	svc, _ := f.service(t, true)

	_, _, _, _, _, err := svc.VerifySessionToken(context.Background(), f.sign(t, f.claims(), nil), true)

	require.ErrorIs(t, err, auth.ErrInvalidToken)
}

func TestVerifySessionToken_LookupFailureIsNotAnAuthRejection(t *testing.T) {
	f := newSessionIssuerFixture(t)
	svc, repo := f.service(t, true)
	repo.override = sql.ErrConnDone

	_, _, _, _, _, err := svc.VerifySessionToken(context.Background(), f.sign(t, f.claims(), nil), true)

	require.ErrorIs(t, err, sql.ErrConnDone, "a database outage must surface as 503, not a terminal 401")
}

func TestVerifySessionToken_IssuerRegisteredAfterAMissAuthenticatesImmediately(t *testing.T) {
	f := newSessionIssuerFixture(t)
	repo := &fakeAPIKeyRepository{byHash: map[string]fakeKeyRow{}}
	cache := auth.NewLRUAPIKeyCache(100, 100, 5*time.Minute, time.Minute)
	svc := auth.NewService(&fakeInstallationRepository{}, repo, nil, nil, cache, nil, func() time.Time { return sessionClock }).WithSessionTokens(true)
	token := f.sign(t, f.claims(), nil)

	_, _, _, _, _, err := svc.VerifySessionToken(context.Background(), token, true)
	require.ErrorIs(t, err, auth.ErrInvalidToken)

	repo.byHash[f.apiKey.KeyHash] = fakeKeyRow{apiKey: f.apiKey, installation: f.inst}
	_, _, _, _, session, err := svc.VerifySessionToken(context.Background(), token, true)
	require.NoError(t, err, "a token signed before its issuer was visible must work once the issuer is registered")
	assert.Equal(t, "session-1", session.SessionID)
}

type countingKeyRepo struct {
	*fakeAPIKeyRepository
	mu      sync.Mutex
	lookups int
}

func (r *countingKeyRepo) GetActiveByHashWithInstallation(ctx context.Context, keyHash string) (*auth.APIKey, *auth.Installation, error) {
	r.mu.Lock()
	r.lookups++
	r.mu.Unlock()
	return r.fakeAPIKeyRepository.GetActiveByHashWithInstallation(ctx, keyHash)
}

func TestVerifySessionToken_ManySessionsShareOneIssuerLookup(t *testing.T) {
	f := newSessionIssuerFixture(t)
	repo := &countingKeyRepo{fakeAPIKeyRepository: &fakeAPIKeyRepository{byHash: map[string]fakeKeyRow{f.apiKey.KeyHash: {apiKey: f.apiKey, installation: f.inst}}}}
	cache := auth.NewLRUAPIKeyCache(100, 100, 5*time.Minute, time.Minute)
	svc := auth.NewService(&fakeInstallationRepository{}, repo, nil, nil, cache, nil, func() time.Time { return sessionClock }).WithSessionTokens(true)

	for i := range 500 {
		c := f.claims()
		c.Subject = "session-" + string(rune('a'+i%26)) + time.Duration(i).String()
		_, _, _, _, _, err := svc.VerifySessionToken(context.Background(), f.sign(t, c, nil), true)
		require.NoError(t, err)
	}

	assert.Equal(t, 1, repo.lookups, "sessions must not add per-session cache entries or database reads")
}

func TestVerifyAPIKey_RejectsSessionTokenAndIssuerAsBearer(t *testing.T) {
	f := newSessionIssuerFixture(t)
	svc, _ := f.service(t, true)

	_, _, _, _, err := svc.VerifyAPIKey(context.Background(), f.sign(t, f.claims(), nil))
	require.ErrorIs(t, err, auth.ErrInvalidToken)

	_, _, _, _, err = svc.VerifyAPIKey(context.Background(), "rk_"+f.kid)
	require.ErrorIs(t, err, auth.ErrInvalidToken, "an issuer row is stored under jwk:<kid>, which no bearer hashes to")
}

func TestSanitizeInboundAuthHeader_ScrubsSessionTokens(t *testing.T) {
	f := newSessionIssuerFixture(t)
	assert.Empty(t, httputil.SanitizeInboundAuthHeader("Bearer "+f.sign(t, f.claims(), nil)))
}

func TestParseSessionIssuerJWK_AcceptsOnlyEd25519PublicKeys(t *testing.T) {
	f := newSessionIssuerFixture(t)
	privateJWK, err := jose.JSONWebKey{Key: f.privateKey}.MarshalJSON()
	require.NoError(t, err)
	symmetric, err := json.Marshal(map[string]string{"kty": "oct", "k": "c2VjcmV0"})
	require.NoError(t, err)

	_, _, _, err = auth.ParseSessionIssuerJWK(privateJWK)
	require.ErrorIs(t, err, auth.ErrInvalidSessionIssuerKey, "private key material must never be registered")
	_, _, _, err = auth.ParseSessionIssuerJWK(symmetric)
	require.ErrorIs(t, err, auth.ErrInvalidSessionIssuerKey)

	_, kid, canonical, err := auth.ParseSessionIssuerJWK(f.publicJWK)
	require.NoError(t, err)
	assert.Equal(t, f.kid, kid)
	assert.NotContains(t, string(canonical), `"d"`)
}

func TestRegisterSessionIssuer_IdempotentAndInstallationBound(t *testing.T) {
	f := newSessionIssuerFixture(t)
	repo := &fakeAPIKeyRepository{byHash: map[string]fakeKeyRow{}, echoCreate: true}
	svc := auth.NewService(&fakeInstallationRepository{}, repo, nil, nil, auth.NoOpAPIKeyCache{}, nil, func() time.Time { return sessionClock })

	key, kid, err := svc.RegisterSessionIssuer(context.Background(), f.inst.ID, nil, f.publicJWK)
	require.NoError(t, err)
	assert.Equal(t, f.kid, kid)
	assert.Equal(t, auth.ScopeSessionIssuer, key.Scope)
	assert.Equal(t, "jwk:"+f.kid, key.KeyHash)

	repo.byHash[key.KeyHash] = fakeKeyRow{apiKey: key, installation: f.inst}
	again, _, err := svc.RegisterSessionIssuer(context.Background(), f.inst.ID, nil, f.publicJWK)
	require.NoError(t, err)
	assert.Same(t, key, again)

	_, _, err = svc.RegisterSessionIssuer(context.Background(), "other-installation", nil, f.publicJWK)
	require.ErrorIs(t, err, auth.ErrSessionIssuerConflict)
}

func TestIssueScopedAPIKey_RefusesSessionIssuerScope(t *testing.T) {
	svc, _ := makeService(t)
	_, _, err := svc.IssueScopedAPIKey(context.Background(), "inst-1", auth.ScopeSessionIssuer, nil, nil)
	require.ErrorIs(t, err, auth.ErrInvalidKeyScope)
}
