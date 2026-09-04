package proxy_test

import (
	"context"
	"net/http"
	"testing"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/proxy"

	"github.com/stretchr/testify/assert"
)

func requestWithCredentials(t *testing.T, creds *proxy.Credentials) *http.Request {
	t.Helper()
	ctx := context.WithValue(context.Background(), proxy.CredentialsContextKey{}, creds)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://acct.example.com/api", nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func TestApplyWIFTokenType_MarksAttestationBearer(t *testing.T) {
	req := requestWithCredentials(t, &proxy.Credentials{APIKey: []byte("WIF.GCP.tok"), AuthType: auth.AuthTypeWIF})

	proxy.ApplyWIFTokenType(req.Context(), req)

	assert.Equal(t, auth.WIFTokenTypeValue, req.Header.Get(auth.WIFTokenTypeHeader),
		"without this header the upstream reads the attestation as one of its own tokens and rejects it")
}

func TestApplyWIFTokenType_OverridesClientSuppliedValue(t *testing.T) {
	req := requestWithCredentials(t, &proxy.Credentials{APIKey: []byte("WIF.GCP.tok"), AuthType: auth.AuthTypeWIF})
	req.Header.Set(auth.WIFTokenTypeHeader, "OAUTH")

	proxy.ApplyWIFTokenType(req.Context(), req)

	assert.Equal(t, auth.WIFTokenTypeValue, req.Header.Get(auth.WIFTokenTypeHeader),
		"a forwarded client header must not be able to change how the router's own credential is read")
}

func TestApplyWIFTokenType_LeavesOtherAuthTypesAlone(t *testing.T) {
	for _, authType := range []string{auth.AuthTypeBearer, auth.AuthTypeKeypairJWT, auth.AuthTypeAzureEntra, ""} {
		req := requestWithCredentials(t, &proxy.Credentials{APIKey: []byte("secret"), AuthType: authType})

		proxy.ApplyWIFTokenType(req.Context(), req)

		assert.Empty(t, req.Header.Get(auth.WIFTokenTypeHeader),
			"only a workload attestation may claim to be one")
	}
}

func TestApplyWIFTokenType_NoCredentials(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://acct.example.com/api", nil)
	if err != nil {
		t.Fatal(err)
	}

	proxy.ApplyWIFTokenType(req.Context(), req)

	assert.Empty(t, req.Header.Get(auth.WIFTokenTypeHeader))
}
