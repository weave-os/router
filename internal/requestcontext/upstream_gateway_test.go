package requestcontext_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/requestcontext"
)

func gatewayCtx(t *testing.T, creds *requestcontext.Credentials, headers requestcontext.UpstreamGatewayHeaders, inbound http.Header) context.Context {
	t.Helper()
	ctx := context.WithValue(context.Background(), requestcontext.CredentialsContextKey{}, creds)
	ctx = requestcontext.WithUpstreamGateway(ctx, headers, inbound)
	return requestcontext.WithClientIdentity(ctx, requestcontext.ClientIdentity{
		Email:       "engineer@example.com",
		DisplayName: "Engineer",
		AccountID:   "account-1",
		DeviceID:    "device-1",
		SessionID:   "metadata-session",
		ClientApp:   "claude-code",
	})
}

func TestUpstreamGatewaySessionID(t *testing.T) {
	t.Run("first configured header wins", func(t *testing.T) {
		h := http.Header{}
		h.Set("X-Gateway-Session", "gw-1")
		h.Set(requestcontext.ClaudeCodeSessionHeader, "cc-1")
		ctx := gatewayCtx(t, &requestcontext.Credentials{}, requestcontext.UpstreamGatewayHeaders{Session: []string{"X-Missing", "X-Gateway-Session"}}, h)
		assert.Equal(t, "gw-1", requestcontext.ClientIdentityFrom(ctx).SessionID)
	})
	t.Run("falls back to the Claude Code session header", func(t *testing.T) {
		h := http.Header{}
		h.Set(requestcontext.ClaudeCodeSessionHeader, "cc-1")
		ctx := gatewayCtx(t, &requestcontext.Credentials{}, requestcontext.UpstreamGatewayHeaders{Session: []string{"X-Gateway-Session"}}, h)
		assert.Equal(t, "cc-1", requestcontext.ClientIdentityFrom(ctx).SessionID)
	})
	t.Run("missing session serves unpinned", func(t *testing.T) {
		ctx := gatewayCtx(t, &requestcontext.Credentials{}, requestcontext.UpstreamGatewayHeaders{}, http.Header{})
		assert.Empty(t, requestcontext.ClientIdentityFrom(ctx).SessionID,
			"a body metadata session must not stand in for the gateway session")
	})
	t.Run("oversized session header falls through", func(t *testing.T) {
		h := http.Header{}
		h.Set("X-Gateway-Session", strings.Repeat("s", requestcontext.MaxClientIdentifierLen+1))
		h.Set(requestcontext.ClaudeCodeSessionHeader, "cc-1")
		ctx := gatewayCtx(t, &requestcontext.Credentials{}, requestcontext.UpstreamGatewayHeaders{Session: []string{"X-Gateway-Session"}}, h)
		assert.Equal(t, "cc-1", requestcontext.ClientIdentityFrom(ctx).SessionID,
			"gateway session IDs get the same length cap as every other client session source")
	})
}

func TestUpstreamGatewayStripsEndUserIdentity(t *testing.T) {
	ctx := gatewayCtx(t, &requestcontext.Credentials{}, requestcontext.UpstreamGatewayHeaders{}, http.Header{})
	id := requestcontext.ClientIdentityFrom(ctx)
	assert.Empty(t, id.Email)
	assert.Empty(t, id.DisplayName)
	assert.Empty(t, id.AccountID)
	assert.Empty(t, id.DeviceID)
	assert.Equal(t, "claude-code", id.ClientApp)
}

func TestUpstreamGatewayUserFromConfiguredHeaders(t *testing.T) {
	headers := requestcontext.UpstreamGatewayHeaders{
		UserEmail: []string{"X-Missing", "X-Gateway-User-Email"},
		UserName:  []string{"X-Gateway-User-Name"},
	}
	inbound := http.Header{}
	inbound.Set("X-Gateway-User-Email", " Gateway.User@Example.com ")
	inbound.Set("X-Gateway-User-Name", "Gateway User")
	creds := &requestcontext.Credentials{
		ForwardedClientHeaders: []string{"traceparent"},
		BaggageHeader:          "X-Gateway-Baggage",
		IdentityHeader:         "X-Gateway-Identity",
	}
	ctx := gatewayCtx(t, creds, headers, inbound)

	id := requestcontext.ClientIdentityFrom(ctx)
	assert.Equal(t, "gateway.user@example.com", id.Email, "the gateway-asserted user replaces the client-supplied one")
	assert.Equal(t, "Gateway User", id.DisplayName)
	assert.Empty(t, id.AccountID)
	assert.Empty(t, id.DeviceID)
	assert.Equal(t, "gateway.user@example.com", requestcontext.CallerFrom(ctx).Email, "dashboards attribute the turn to the gateway user")

	upstream := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/messages", nil)
	requestcontext.ApplyIdentityHeader(ctx, upstream)
	requestcontext.ApplyForwardedClientHeaders(ctx, upstream, inbound)
	for name, values := range upstream.Header {
		for _, v := range values {
			assert.NotContains(t, strings.ToLower(v), "gateway.user@example.com", "header %s must not carry the user upstream", name)
		}
	}
}

func TestUpstreamGatewayUserHeaderMissingOrInvalidIsUnidentified(t *testing.T) {
	headers := requestcontext.UpstreamGatewayHeaders{UserEmail: []string{"X-Gateway-User-Email"}}
	for name, value := range map[string]string{"missing": "", "not an email": "gateway-user", "oversized": strings.Repeat("a", requestcontext.MaxEmailLen) + "@example.com"} {
		t.Run(name, func(t *testing.T) {
			inbound := http.Header{}
			if value != "" {
				inbound.Set("X-Gateway-User-Email", value)
			}
			inbound.Set("X-Weave-User-Email", "spoofed@example.com")
			id := requestcontext.ClientIdentityFrom(gatewayCtx(t, &requestcontext.Credentials{}, headers, inbound))
			assert.Empty(t, id.Email, "only the configured gateway header can identify the user")
			assert.Empty(t, id.DisplayName)
		})
	}
}

func TestUpstreamGatewayInnerCallHeaders(t *testing.T) {
	creds := &requestcontext.Credentials{
		ForwardedClientHeaders: []string{"traceparent", "X-Gateway-Request-Id"},
		BaggageHeader:          "X-Gateway-Baggage",
		IdentityHeader:         "X-Gateway-Identity",
	}
	inbound := http.Header{}
	inbound.Set("traceparent", "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01")
	inbound.Set("X-Gateway-Request-Id", "req-1")
	inbound.Set("X-Gateway-Baggage", "user=engineer@example.com")
	inbound.Set("Authorization", "Bearer inbound-gateway-token")
	ctx := gatewayCtx(t, creds, requestcontext.UpstreamGatewayHeaders{}, inbound)

	upstream := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/messages", nil)
	requestcontext.ApplyIdentityHeader(ctx, upstream)
	requestcontext.ApplyForwardedClientHeaders(ctx, upstream, inbound)

	assert.Equal(t, inbound.Get("traceparent"), upstream.Header.Get("traceparent"))
	assert.Equal(t, "req-1", upstream.Header.Get("X-Gateway-Request-Id"))
	assert.Empty(t, upstream.Header.Get("X-Gateway-Baggage"), "gateway mode sends no baggage")
	assert.Empty(t, upstream.Header.Get("X-Gateway-Identity"), "gateway mode sends no identity")
	assert.Empty(t, upstream.Header.Get("Authorization"), "the inbound credential never crosses the hop")
	assert.Equal(t, "1", upstream.Header.Get(requestcontext.UpstreamGatewayHopHeader))
}

func TestOutsideGatewayModeNoHopMarker(t *testing.T) {
	ctx := identityCtx(&requestcontext.Credentials{}, requestcontext.ClientIdentity{})
	upstream := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/messages", nil)
	requestcontext.ApplyForwardedClientHeaders(ctx, upstream, http.Header{})
	assert.Empty(t, upstream.Header.Get(requestcontext.UpstreamGatewayHopHeader))
	_, ok := requestcontext.UpstreamGatewayFrom(ctx)
	assert.False(t, ok)
}
