package proxy

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"
)

func TestResponseProtocolSwitchUsesActualAttemptProvider(t *testing.T) {
	for _, provider := range []string{providers.ProviderOpenAI, providers.ProviderAnthropic, providers.ProviderGoogle, providers.ProviderOpenRouter} {
		t.Run(provider, func(t *testing.T) {
			writer := translate.NewResponsesWriter(httptest.NewRecorder(), "synthetic")
			switcher := responseProtocolSwitch{writer: writer}
			switcher.native(false)
			called := false
			build := switcher.wrapBuilder(func(router.Decision, translate.EmitOptions, string, context.Context) (dispatchAttempt, error) {
				return func(_ context.Context, _ router.Decision, _ providers.Client) error {
					called = true
					require.Equal(t, provider == providers.ProviderOpenAI, writer.Passthrough())
					return nil
				}, nil
			})
			attempt, err := build(router.Decision{Provider: providers.ProviderOpenAI}, translate.EmitOptions{}, "", context.Background())
			require.NoError(t, err)
			require.NoError(t, attempt(context.Background(), router.Decision{Provider: provider}, nil))
			require.True(t, called)
		})
	}
}

func TestResponseProtocolSwitchRejectsCommittedNativeOutputBeforeBuilding(t *testing.T) {
	writer := translate.NewResponsesWriter(httptest.NewRecorder(), "synthetic")
	switcher := responseProtocolSwitch{writer: writer}
	switcher.native(false)
	writer.Header().Set("Content-Type", "text/event-stream")
	_, err := writer.Write([]byte(`data: {"type":"response.created","response":{"id":"resp_synthetic"}}` + "\n\n"))
	require.NoError(t, err)
	built := false
	build := switcher.wrapBuilder(func(router.Decision, translate.EmitOptions, string, context.Context) (dispatchAttempt, error) {
		built = true
		return nil, nil
	})
	_, err = build(router.Decision{Provider: providers.ProviderAnthropic}, translate.EmitOptions{}, "", context.Background())
	require.ErrorIs(t, err, errResponsesPassthroughStuck)
	require.False(t, built)
	require.True(t, writer.Passthrough())
}
