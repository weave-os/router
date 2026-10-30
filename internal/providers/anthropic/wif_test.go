package anthropic_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers/anthropic"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
)

func TestWorkloadIdentityAuthKeepsAnthropicAndSnowflakeSeparate(t *testing.T) {
	for _, authType := range []string{auth.AuthTypeAnthropicWIF, auth.AuthTypeWIF} {
		t.Run(authType, func(t *testing.T) {
			var headers http.Header
			var body []byte
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				headers = r.Header.Clone()
				body, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"msg_test"}`))
			}))
			defer upstream.Close()
			var client *anthropic.Client
			token := "sk-ant-oat01-federated-token"
			if authType == auth.AuthTypeWIF {
				token = "WIF.GCP.snowflake-attestation"
				client = anthropic.NewClient("deployment-token", upstream.URL, anthropic.WithAuthScheme(anthropic.AuthBearer))
			} else {
				client = anthropic.NewClient("deployment-token", upstream.URL)
			}
			ctx := requestcontext.WithCredentials(context.Background(), &requestcontext.Credentials{AuthType: authType, APIKey: []byte(token), Source: requestcontext.SourceBYOK})
			prep, inbound := messagesRequest()
			require.NoError(t, client.Proxy(ctx, router.Decision{Model: "claude-opus-5"}, prep, httptest.NewRecorder(), inbound))
			assert.Equal(t, "Bearer "+token, headers.Get("Authorization"))
			assert.Empty(t, headers.Get("x-api-key"))
			assert.NotContains(t, headers.Get("anthropic-beta"), "oauth-2025-04-20")
			assert.NotContains(t, string(body), "You are Claude Code")
			if authType == auth.AuthTypeWIF {
				assert.Equal(t, auth.WIFTokenTypeValue, headers.Get(auth.WIFTokenTypeHeader))
			} else {
				assert.Empty(t, headers.Get(auth.WIFTokenTypeHeader))
			}
		})
	}
}
