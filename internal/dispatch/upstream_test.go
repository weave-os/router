package dispatch_test

import (
	"context"
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
		providers.PreparedRequest{}, httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/messages", nil))

	require.ErrorIs(t, err, sentinel)
	assert.Equal(t, []string{"kimi-k2.5"}, client.models)
}

func TestUpstreamKeepsTargetGuard(t *testing.T) {
	client := &fakeUpstream{}
	err := dispatch.NewUpstream(dispatch.GuardTarget(client, primary)).Proxy(context.Background(), router.Decision{Provider: providers.ProviderOpenRouter, Model: "kimi-k2.5"},
		providers.PreparedRequest{}, httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/messages", nil))

	require.ErrorIs(t, err, dispatch.ErrTargetMismatch)
	assert.Empty(t, client.models)
}
