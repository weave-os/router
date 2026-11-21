package requestcontext_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/requestcontext"
)

func gatewayCtx(t *testing.T, creds *requestcontext.Credentials, sessionHeaders []string, inbound http.Header) context.Context {
	t.Helper()
	ctx := context.WithValue(context.Background(), requestcontext.CredentialsContextKey{}, creds)
	ctx = requestcontext.WithUpstreamGateway(ctx, sessionHeaders, inbound)
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
		ctx := gatewayCtx(t, &requestcontext.Credentials{}, []string{"X-Missing", "X-Gateway-Session"}, h)
		assert.Equal(t, "gw-1", requestcontext.ClientIdentityFrom(ctx).SessionID)
	})
	t.Run("falls back to the Claude Code session header", func(t *testing.T) {
		h := http.Header{}
		h.Set(requestcontext.ClaudeCodeSessionHeader, "cc-1")
		ctx := gatewayCtx(t, &requestcontext.Credentials{}, []string{"X-Gateway-Session"}, h)
		assert.Equal(t, "cc-1", requestcontext.ClientIdentityFrom(ctx).SessionID)
	})
	t.Run("missing session serves unpinned", func(t *testing.T) {
		ctx := gatewayCtx(t, &requestcontext.Credentials{}, nil, http.Header{})
		assert.Empty(t, requestcontext.ClientIdentityFrom(ctx).SessionID,
			"a body metadata session must not stand in for the gateway session")
	})
}

func TestUpstreamGatewayStripsEndUserIdentity(t *testing.T) {
	ctx := gatewayCtx(t, &requestcontext.Credentials{}, nil, http.Header{})
	id := requestcontext.ClientIdentityFrom(ctx)
	assert.Empty(t, id.Email)
	assert.Empty(t, id.DisplayName)
	assert.Empty(t, id.AccountID)
	assert.Empty(t, id.DeviceID)
	assert.Equal(t, "claude-code", id.ClientApp)
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
	ctx := gatewayCtx(t, creds, nil, inbound)

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
