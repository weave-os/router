package auth_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"
	"time"

	"weave-os/router/internal/auth"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testKey is a 2048-bit RSA key shared by the tests that don't need their own;
// generating one per test dominates the suite's runtime.
var testKey = mustGenerateKey(2048)

func mustGenerateKey(bits int) *rsa.PrivateKey {
	key, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		panic(err)
	}
	return key
}

// pkcs8PEM encodes key in the PKCS#8 PEM form Snowflake's docs produce.
func pkcs8PEM(t *testing.T, key *rsa.PrivateKey) []byte {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

// decodeClaims returns the JWT's payload claims without verifying the signature.
func decodeClaims(t *testing.T, token string) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	require.Len(t, parts, 3, "a signed JWT must have three dot-separated segments")
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	var claims map[string]any
	require.NoError(t, json.Unmarshal(payload, &claims))
	return claims
}

func TestMintKeypairJWT_ClaimsMatchSnowflakeFormat(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	token, err := auth.MintKeypairJWT(testKey, "myorg-myaccount", "service_user", now, auth.KeypairTokenTTL)
	require.NoError(t, err)

	fingerprint, err := auth.PublicKeyFingerprint(&testKey.PublicKey)
	require.NoError(t, err)
	claims := decodeClaims(t, token)

	assert.Equal(t, "MYORG-MYACCOUNT.SERVICE_USER", claims["sub"],
		"the subject must be the uppercased ACCOUNT.USER pair Snowflake matches against the assigned key")
	assert.Equal(t, "MYORG-MYACCOUNT.SERVICE_USER."+fingerprint, claims["iss"],
		"the issuer must bind the public-key fingerprint so a rotated key stops authenticating")
	assert.InDelta(t, float64(now.Add(auth.KeypairTokenTTL).Unix()), claims["exp"], 1,
		"exp must be the requested expiry")
	assert.Less(t, claims["iat"].(float64), float64(now.Unix()),
		"iat must be backdated so a signer running ahead of Snowflake's clock still produces a usable token")
}

func TestMintKeypairJWT_LifetimeStaysUnderSnowflakeCeiling(t *testing.T) {
	now := time.Now()
	token, err := auth.MintKeypairJWT(testKey, "acct", "user", now, auth.KeypairTokenTTL)
	require.NoError(t, err)

	claims := decodeClaims(t, token)
	lifetime := time.Duration(claims["exp"].(float64)-claims["iat"].(float64)) * time.Second
	assert.Less(t, lifetime, time.Hour,
		"Snowflake clamps key-pair JWTs to one hour, so a token must never claim a longer window than it can hold")
}

func TestMintKeypairJWT_ClampsOverlongTTL(t *testing.T) {
	now := time.Now()
	token, err := auth.MintKeypairJWT(testKey, "acct", "user", now, 24*time.Hour)
	require.NoError(t, err)

	claims := decodeClaims(t, token)
	lifetime := time.Duration(claims["exp"].(float64)-claims["iat"].(float64)) * time.Second
	assert.LessOrEqual(t, lifetime, time.Hour,
		"a caller asking for more than the upstream ceiling must get a token clamped to it, not one rejected on arrival")
}

func TestPublicKeyFingerprint_MatchesSnowflakeEncoding(t *testing.T) {
	fingerprint, err := auth.PublicKeyFingerprint(&testKey.PublicKey)
	require.NoError(t, err)

	encoded, found := strings.CutPrefix(fingerprint, "SHA256:")
	require.True(t, found, "Snowflake reports RSA_PUBLIC_KEY_FP with a SHA256: prefix, got %q", fingerprint)
	raw, err := base64.StdEncoding.DecodeString(encoded)
	require.NoError(t, err, "the fingerprint body must be standard base64")
	assert.Len(t, raw, 32, "the digest must be a SHA-256 of the DER SPKI encoding")
}

func TestParseKeypairPrivateKey_AcceptsPKCS8AndPKCS1(t *testing.T) {
	pkcs1 := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(testKey),
	})
	for name, encoded := range map[string][]byte{
		"pkcs8": pkcs8PEM(t, testKey),
		"pkcs1": pkcs1,
	} {
		t.Run(name, func(t *testing.T) {
			parsed, err := auth.ParseKeypairPrivateKey(encoded)
			require.NoError(t, err)
			assert.Equal(t, testKey.N, parsed.N, "the parsed key must be the one that was encoded")
		})
	}
}

func TestParseKeypairPrivateKey_RejectsUnusableKeys(t *testing.T) {
	small := pkcs8PEM(t, mustGenerateKey(1024))
	encrypted := pem.EncodeToMemory(&pem.Block{
		Type:    "ENCRYPTED PRIVATE KEY",
		Headers: map[string]string{"Proc-Type": "4,ENCRYPTED"},
		Bytes:   []byte("not-a-key"),
	})
	for name, encoded := range map[string][]byte{
		"not pem":         []byte("sk-not-a-private-key"),
		"under 2048 bits": small,
		"passphrase":      encrypted,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := auth.ParseKeypairPrivateKey(encoded)
			require.ErrorIs(t, err, auth.ErrInvalidKeypairAuth,
				"an unusable private key must be rejected at configuration time, not at the first upstream 401")
		})
	}
}

func TestNormalizeAuthType_UppercasesPrincipalAndStripsRegion(t *testing.T) {
	account, user := "xy12345.us-east-1.aws", "service_user"
	authType, gotAccount, gotUser, err := auth.NormalizeAuthType("keypair_jwt", &account, &user)
	require.NoError(t, err)

	assert.Equal(t, auth.AuthTypeKeypairJWT, authType)
	require.NotNil(t, gotAccount)
	require.NotNil(t, gotUser)
	assert.Equal(t, "XY12345", *gotAccount,
		"an account locator's region suffixes are not part of the identifier the JWT claims carry")
	assert.Equal(t, "SERVICE_USER", *gotUser)
}

func TestNormalizeAuthType_Rejects(t *testing.T) {
	value := "MYORG-MYACCOUNT"
	cases := map[string]struct {
		authType      string
		account, user *string
	}{
		"unknown type":                {authType: "oauth"},
		"keypair without principal":   {authType: "keypair_jwt"},
		"keypair without user":        {authType: "keypair_jwt", account: &value},
		"bearer carrying a principal": {authType: "bearer", account: &value, user: &value},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, _, err := auth.NormalizeAuthType(tc.authType, tc.account, tc.user)
			require.ErrorIs(t, err, auth.ErrInvalidKeypairAuth)
		})
	}
}

func TestNormalizeAuthType_EmptyTypeDefaultsToBearer(t *testing.T) {
	authType, account, user, err := auth.NormalizeAuthType("", nil, nil)
	require.NoError(t, err)
	assert.Equal(t, auth.AuthTypeBearer, authType,
		"a request that says nothing about auth must keep today's send-the-secret behavior")
	assert.Nil(t, account)
	assert.Nil(t, user)
}
