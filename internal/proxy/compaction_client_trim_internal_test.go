package proxy

import (
	"context"
	"net/http"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"
)

// Claude Code trims the oldest rounds and retries a compaction only when the
// error detail matches this pattern; it reads the token gap from the numbers.
var claudeCodePromptTooLong = regexp.MustCompile(`(?i)^prompt is too long[^0-9]*(\d+)\s*tokens?\s*>\s*(\d+)`)

func lunaOverflow() error {
	return &providers.UpstreamErrorResponse{
		Status: http.StatusBadRequest,
		Body:   []byte(`{"error":{"message":"Your input exceeds the context window of this model. Please adjust your input and try again.","type":"invalid_request_error","param":"input","code":"context_length_exceeded"}}`),
	}
}

func forcedOpusGatewayCompactionFixture(anthropicFail error) (*Service, context.Context) {
	anthropicGateway := &gatewayCompactionClient{provider: providers.ProviderAnthropicGateway, failWith: anthropicFail}
	openAIGateway := &gatewayCompactionClient{provider: providers.ProviderOpenAIGateway, failWith: lunaOverflow()}
	store := &forcedPinStore{pin: sessionpin.Pin{
		Provider:    providers.ProviderAnthropicGateway,
		Model:       gatewayCompactionOpusModel,
		Reason:      translate.ReasonUserForceModel,
		PinnedUntil: time.Now().Add(time.Hour),
	}}
	svc := NewService(nil, map[string]providers.Client{
		providers.ProviderAnthropicGateway: anthropicGateway,
		providers.ProviderOpenAIGateway:    openAIGateway,
	}, nil, false, nil, store, false, providers.ProviderAnthropic, policy.PrecompactionDefaultModel, nil).
		WithCompactionHardPin(true).
		WithRetrySleep(func(context.Context, time.Duration) error { return nil })
	ctx := rescuedFailureCtx()
	ctx = context.WithValue(ctx, InstallationAllowedModelsContextKey{}, []string{catalog.ModelGPT6Luna, gatewayCompactionOpusModel})
	ctx = context.WithValue(ctx, ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{
		gatewayKey(providers.ProviderAnthropicGateway, "https://gateway.example.com/anthropic", gatewayCompactionOpusModel, policy.PrecompactionDefaultModel),
		gatewayKey(providers.ProviderOpenAIGateway, "https://gateway.example.com/openai", catalog.ModelGPT6Luna),
	})
	return svc, ctx
}

// When no reachable model can summarize the transcript, Claude Code must get
// a prompt-too-long it can act on (drop the oldest rounds and retry the
// compaction), not a terminal 502 that ends the session.
func TestCompactionOverflowingEverywhereLetsClaudeCodeTrimAndRetry(t *testing.T) {
	cases := map[string]func() (*Service, context.Context){
		"automatic": func() (*Service, context.Context) {
			svc, ctx, _, openAIGateway := gatewayCompactionFixture(cortexPromptTooLong())
			openAIGateway.failWith = lunaOverflow()
			return svc, ctx
		},
		"user_forced_opus": func() (*Service, context.Context) { return forcedOpusGatewayCompactionFixture(cortexPromptTooLong()) },
	}
	for name, fixture := range cases {
		t.Run(name, func(t *testing.T) {
			svc, ctx := fixture()
			ctx = context.WithValue(ctx, ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppClaudeCode})

			_, err := proxyGatewayCompaction(t, svc, ctx)

			require.Error(t, err)
			class, ok := ClassifyDispatchError(err)
			require.True(t, ok)
			assert.Equal(t, http.StatusBadRequest, class.Status)
			m := claudeCodePromptTooLong.FindStringSubmatch(class.Message)
			require.NotNil(t, m, "client-visible message %q", class.Message)
			assert.Equal(t, "2100056", m[1])
			assert.Equal(t, "1000000", m[2])
			assert.NotContains(t, class.Message, "Compact the conversation")
		})
	}
}

// A prompt-too-long without counts, or one sent to a client that does not trim
// from them, would bring back the compaction retry loop, so these stay a 502.
func TestCompactionOverflowingEverywhereStaysTerminalWithoutTrimCounts(t *testing.T) {
	countless := &providers.UpstreamErrorResponse{
		Status: http.StatusBadRequest,
		Body:   []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long"}}`),
	}
	cases := map[string]struct {
		clientApp     string
		anthropicFail error
	}{
		"claude_code_without_counts": {clientApp: ClientAppClaudeCode, anthropicFail: countless},
		"other_client_with_counts":   {clientApp: "opencode", anthropicFail: cortexPromptTooLong()},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			svc, ctx := forcedOpusGatewayCompactionFixture(tc.anthropicFail)
			ctx = context.WithValue(ctx, ClientIdentityContextKey{}, ClientIdentity{ClientApp: tc.clientApp})

			_, err := proxyGatewayCompaction(t, svc, ctx)

			class, ok := ClassifyDispatchError(err)
			require.True(t, ok)
			assert.Equal(t, DispatchErrorCompactionUnservable, class.Kind)
			assert.Equal(t, http.StatusBadGateway, class.Status)
			assert.True(t, class.NoClientRetry)
			assert.NotContains(t, class.Message, "prompt is too long")
		})
	}
}
