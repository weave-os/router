package proxy

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/dispatch"
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
			build := switcher.wrapBuilder(func(router.Decision, translate.EmitOptions, string, context.Context) (dispatch.UpstreamAttempt, error) {
				return func(_ context.Context, _ router.Decision, _ dispatch.Upstream) error {
					called = true
					require.Equal(t, provider == providers.ProviderOpenAI, writer.Passthrough())
					return nil
				}, nil
			})
			attempt, err := build(router.Decision{Provider: providers.ProviderOpenAI}, translate.EmitOptions{}, "", context.Background())
			require.NoError(t, err)
			require.NoError(t, attempt(context.Background(), router.Decision{Provider: provider}, dispatch.Upstream{}))
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
	build := switcher.wrapBuilder(func(router.Decision, translate.EmitOptions, string, context.Context) (dispatch.UpstreamAttempt, error) {
		built = true
		return nil, nil
	})
	_, err = build(router.Decision{Provider: providers.ProviderAnthropic}, translate.EmitOptions{}, "", context.Background())
	require.ErrorIs(t, err, errResponsesPassthroughStuck)
	require.False(t, built)
	require.True(t, writer.Passthrough())
}

func TestResponseProtocolSwitchRejectsCommittedBadgeOutput(t *testing.T) {
	buffer := newPreludeBuffer(httptest.NewRecorder())
	writer := translate.NewResponsesWriter(buffer, "synthetic")
	switcher := responseProtocolSwitch{writer: writer, buffer: buffer}
	switcher.native(true)
	require.NoError(t, writer.Prelude(true))
	writer.Flush()
	require.NoError(t, buffer.CommitPrelude())
	buffer.Seal()
	_, err := writer.Write([]byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":1,\"output_index\":0,\"content_index\":0,\"delta\":\"already delivered\"}\n\n"))
	require.NoError(t, err)
	require.True(t, buffer.Committed())
	require.ErrorIs(t, switcher.selectTarget(router.Decision{Provider: providers.ProviderAnthropic}), errResponsesPassthroughStuck)
	require.ErrorIs(t, switcher.selectTarget(router.Decision{Provider: providers.ProviderOpenAI}), errResponsesPassthroughStuck)
	require.ErrorIs(t, switcher.fallbackToChat(surfaceResponsesTranslated), errResponsesPassthroughStuck)
	require.True(t, writer.Passthrough())
}

func TestResponseProtocolSwitchReturnsToOpenAIThroughTranslation(t *testing.T) {
	buffer := newPreludeBuffer(httptest.NewRecorder())
	writer := translate.NewResponsesWriter(buffer, "synthetic")
	switcher := responseProtocolSwitch{writer: writer, buffer: buffer, nativeRequest: true}
	switcher.native(true)
	require.NoError(t, writer.Prelude(true))
	writer.Flush()
	require.NoError(t, buffer.CommitPrelude())
	require.False(t, buffer.Committed())
	require.Equal(t, surfaceResponsesNative, switcher.surface(providers.ProviderOpenAI, false))

	require.NoError(t, switcher.selectTarget(router.Decision{Provider: providers.ProviderAnthropic}))
	require.False(t, switcher.passthrough())
	require.Equal(t, surfaceChat, switcher.surface(providers.ProviderAnthropic, false))
	require.NoError(t, switcher.selectTarget(router.Decision{Provider: providers.ProviderOpenAI}))
	require.Equal(t, surfaceResponsesTranslated, switcher.surface(providers.ProviderOpenAI, false))
}

func TestResponseProtocolSwitchEndpointFallbackUpdatesExistingBuilder(t *testing.T) {
	writer := translate.NewResponsesWriter(httptest.NewRecorder(), "synthetic")
	switcher := responseProtocolSwitch{writer: writer, nativeRequest: true, promotedToResponses: true}
	switcher.native(false)
	build := switcher.wrapBuilder(func(router.Decision, translate.EmitOptions, string, context.Context) (dispatch.UpstreamAttempt, error) {
		return func(context.Context, router.Decision, dispatch.Upstream) error {
			require.Equal(t, surfaceChat, switcher.surface(providers.ProviderOpenAI, false))
			return nil
		}, nil
	})
	target := router.Decision{Provider: providers.ProviderOpenAI}
	attempt, err := build(target, translate.EmitOptions{}, "", context.Background())
	require.NoError(t, err)
	require.NoError(t, switcher.fallbackToChat(surfaceResponsesNative))
	require.NoError(t, attempt(context.Background(), target, dispatch.Upstream{}))
	require.False(t, writer.Passthrough())
}

func TestResponseProtocolSwitchPreservesNativeOnlyRequest(t *testing.T) {
	writer := translate.NewResponsesWriter(httptest.NewRecorder(), "synthetic")
	switcher := responseProtocolSwitch{writer: writer, nativeRequest: true}
	switcher.native(false)
	require.ErrorIs(t, switcher.fallbackToChat(surfaceResponsesNative), errResponsesPassthroughStuck)
	require.Equal(t, surfaceResponsesNative, switcher.surface(providers.ProviderOpenAI, false))
}
