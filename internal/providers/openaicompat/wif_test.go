package openaicompat_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/openaicompat"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProxy_WIFCredentialsCarryTokenTypeHeader: a workload attestation is only
// read as a federated identity when the token-type header travels with it.
func TestProxy_WIFCredentialsCarryTokenTypeHeader(t *testing.T) {
	var gotAuth, gotTokenType string

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotTokenType = r.Header.Get(auth.WIFTokenTypeHeader)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"chatcmpl-wif","object":"chat.completion"}`))
	}))
	defer upstream.Close()

	c := openaicompat.NewGatewayClient("", upstream.URL+"/api/v2/cortex/v1")
	creds := &proxy.Credentials{
		APIKey:   []byte("WIF.GCP.header.payload.sig"),
		Source:   "byok",
		AuthType: auth.AuthTypeWIF,
	}
	ctx := context.WithValue(context.Background(), proxy.CredentialsContextKey{}, creds)

	rec := httptest.NewRecorder()
	clientReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(""))
	// A client-supplied value must not survive: it would downgrade how the
	// upstream reads the router's own credential.
	prep := providers.PreparedRequest{
		Body:    []byte(`{"model":"m","messages":[]}`),
		Headers: http.Header{auth.WIFTokenTypeHeader: []string{"OAUTH"}},
	}
	err := c.Proxy(ctx, router.Decision{Model: "m"}, prep, rec, clientReq)

	require.NoError(t, err)
	assert.Equal(t, "Bearer WIF.GCP.header.payload.sig", gotAuth)
	assert.Equal(t, auth.WIFTokenTypeValue, gotTokenType)
}
