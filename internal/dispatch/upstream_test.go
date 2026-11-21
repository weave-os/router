package dispatch_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
)

func TestUpstreamProxySendsThroughWrappedClient(t *testing.T) {
	sentinel := assert.AnError
	client := &fakeUpstream{errs: []error{sentinel}}
	err := dispatch.NewUpstream(client).Proxy(context.Background(), router.Decision{Provider: providers.ProviderFireworks, Model: "kimi-k2.5"},
		providers.PreparedRequest{}, httptest.NewRecorder(), httptest.NewRequestWithContext(context.Background(), "POST", "/v1/messages", nil))

	require.ErrorIs(t, err, sentinel)
	assert.Equal(t, []string{"kimi-k2.5"}, client.models)
}

func TestUpstreamKeepsTargetGuard(t *testing.T) {
	client := &fakeUpstream{}
	err := dispatch.NewUpstream(dispatch.GuardTarget(client, primary)).Proxy(context.Background(), router.Decision{Provider: providers.ProviderOpenRouter, Model: "kimi-k2.5"},
		providers.PreparedRequest{}, httptest.NewRecorder(), httptest.NewRequestWithContext(context.Background(), "POST", "/v1/messages", nil))

	require.ErrorIs(t, err, dispatch.ErrTargetMismatch)
	assert.Empty(t, client.models)
}

type passthroughRecorder struct {
	fakeUpstream
	bodies []string
}

func (p *passthroughRecorder) Passthrough(_ context.Context, prep providers.PreparedRequest, _ http.ResponseWriter, _ *http.Request) error {
	p.bodies = append(p.bodies, string(prep.Body))
	return nil
}

func TestClientsUpstreamPassesThroughNamedClient(t *testing.T) {
	client := &passthroughRecorder{}
	upstream, err := dispatch.NewClients(map[string]providers.Client{providers.ProviderAnthropic: client}).Upstream(providers.ProviderAnthropic)
	require.NoError(t, err)

	require.NoError(t, upstream.Passthrough(context.Background(), providers.PreparedRequest{Body: []byte(`{"model":"m"}`)},
		httptest.NewRecorder(), httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/v1/messages/count_tokens", nil)))
	assert.Equal(t, []string{`{"model":"m"}`}, client.bodies)
}

func TestClientsUpstreamRejectsUnconfiguredProvider(t *testing.T) {
	_, err := dispatch.NewClients(nil).Upstream(providers.ProviderAnthropic)
	require.ErrorIs(t, err, dispatch.ErrProviderNotConfigured)
}
