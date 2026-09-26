package iam

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/idtoken"
	"google.golang.org/api/option"
)

type identityCertTransport func(*http.Request) (*http.Response, error)

func (f identityCertTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestServiceIdentityVerifiesSignatureAudienceExpiryIssuerAndSubject(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	encode := base64.RawURLEncoding.EncodeToString
	certs, err := json.Marshal(map[string]any{"keys": []map[string]string{{
		"alg": "RS256", "kid": "fixture", "kty": "RSA", "use": "sig",
		"n": encode(key.N.Bytes()), "e": encode(big.NewInt(int64(key.E)).Bytes()),
	}}})
	require.NoError(t, err)
	validator, err := idtoken.NewValidator(context.Background(), option.WithHTTPClient(&http.Client{Transport: identityCertTransport(func(r *http.Request) (*http.Response, error) {
		assert.Equal(t, "https", r.URL.Scheme)
		assert.Equal(t, "www.googleapis.com", r.URL.Host)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(certs)))}, nil
	})}))
	require.NoError(t, err)
	const audience = "https://router.example/internal/v1/router"
	const backendSubject = "123456789012345678901"
	verifier := &ServiceIdentityVerifier{validator: validator, audience: audience, subjects: map[string]struct{}{backendSubject: {}}}
	for _, test := range []struct {
		name, issuer, subject, audience string
		expired, tampered, accepted     bool
	}{
		{name: "backend", issuer: "https://accounts.google.com", subject: backendSubject, audience: audience, accepted: true},
		{name: "wrong service", issuer: "https://accounts.google.com", subject: "another-service", audience: audience},
		{name: "wrong issuer", issuer: "https://issuer.example", subject: backendSubject, audience: audience},
		{name: "wrong audience", issuer: "https://accounts.google.com", subject: backendSubject, audience: "another-audience"},
		{name: "expired", issuer: "https://accounts.google.com", subject: backendSubject, audience: audience, expired: true},
		{name: "tampered", issuer: "https://accounts.google.com", subject: backendSubject, audience: audience, tampered: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			expiry := time.Now().Add(time.Hour).Unix()
			if test.expired {
				expiry = time.Now().Add(-time.Hour).Unix()
			}
			claims, err := json.Marshal(idtoken.Payload{Issuer: test.issuer, Subject: test.subject, Audience: test.audience, Expires: expiry})
			require.NoError(t, err)
			unsigned := encode([]byte(`{"alg":"RS256","kid":"fixture"}`)) + "." + encode(claims)
			digest := sha256.Sum256([]byte(unsigned))
			signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
			require.NoError(t, err)
			if test.tampered {
				signature[0] ^= 1
			}
			err = verifier.VerifyServiceIdentity(context.Background(), unsigned+"."+encode(signature))
			if test.accepted {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
