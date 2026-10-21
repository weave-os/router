package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
)

const gatewayCompactionOpusModel = "claude-opus-5-5"

// gatewayCompactionClient records each dispatched model and serves or fails
// it in the wire format of its gateway.
type gatewayCompactionClient struct {
	provider string
	failWith error
	models   []string
}

func (c *gatewayCompactionClient) Proxy(_ context.Context, decision router.Decision, _ providers.PreparedRequest, w http.ResponseWriter, _ *http.Request) error {
	c.models = append(c.models, decision.Model)
	if c.failWith != nil {
		return c.failWith
	}
	if c.provider == providers.ProviderOpenAIGateway {
		_, _ = io.WriteString(w, `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"gpt-6-luna","choices":[{"index":0,"delta":{"role":"assistant","content":"summary"},"finish_reason":null}]}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"gpt-6-luna","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		return nil
	}
	_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"role\":\"assistant\",\"model\":\"claude-opus-5-5\",\"content\":[],\"usage\":{\"input_tokens\":5}}}\n\n")
	_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	return nil
}

func (*gatewayCompactionClient) Passthrough(context.Context, providers.PreparedRequest, http.ResponseWriter, *http.Request) error {
	return providers.ErrNotImplemented
}

// A gateway-exclusive tenant (Anthropic models behind
// an Anthropic-spec gateway, OpenAI models behind an OpenAI-spec gateway,
// allowlist of one model each) whose generic utility pin is the fastest model.
func gatewayCompactionFixture(anthropicErr error) (*Service, context.Context, *gatewayCompactionClient, *gatewayCompactionClient) {
	anthropicGateway := &gatewayCompactionClient{provider: providers.ProviderAnthropicGateway, failWith: anthropicErr}
	openAIGateway := &gatewayCompactionClient{provider: providers.ProviderOpenAIGateway}
	fastest := func(HardPinRequest) (string, string, bool) {
		return providers.ProviderOpenAIGateway, catalog.ModelGPT6Luna, true
	}
	svc := NewService(nil, map[string]providers.Client{
		providers.ProviderAnthropicGateway: anthropicGateway,
		providers.ProviderOpenAIGateway:    openAIGateway,
	}, nil, false, nil, &rolePinStore{}, false, providers.ProviderAnthropic, policy.PrecompactionDefaultModel, nil).
		WithHardPinResolver(fastest).
		WithCompactionHardPin(true).
		WithRetrySleep(func(context.Context, time.Duration) error { return nil })
	ctx := rescuedFailureCtx()
	ctx = context.WithValue(ctx, InstallationAllowedModelsContextKey{}, []string{catalog.ModelGPT6Luna, gatewayCompactionOpusModel})
	ctx = context.WithValue(ctx, ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{
		gatewayKey(providers.ProviderAnthropicGateway, "https://gateway.example.com/anthropic", gatewayCompactionOpusModel, policy.PrecompactionDefaultModel),
		gatewayKey(providers.ProviderOpenAIGateway, "https://gateway.example.com/openai", catalog.ModelGPT6Luna),
	})
	return svc, ctx, anthropicGateway, openAIGateway
}

func proxyGatewayCompaction(t *testing.T, svc *Service, ctx context.Context) (*httptest.ResponseRecorder, error) {
	t.Helper()
	body := []byte(`{"model":"claude-opus-5-5","max_tokens":20000,"stream":true,"system":"Your task is to create a detailed summary of the conversation so far.","messages":[{"role":"user","content":"summarize"}]}`)
	rec := httptest.NewRecorder()
	err := svc.ProxyMessages(ctx, body, rec, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(string(body))))
	return rec, err
}

// Gateway tenants previously skipped the summarizer cascade and compacted on
// the fastest utility model even when their gateway aliases a reviewed
// Anthropic summarizer.
func TestGatewayCompactionUsesReachableSummarizer(t *testing.T) {
	svc, ctx, anthropicGateway, openAIGateway := gatewayCompactionFixture(nil)

	rec, err := proxyGatewayCompaction(t, svc, ctx)

	require.NoError(t, err)
	assert.Equal(t, gatewayCompactionOpusModel, rec.Header().Get(HeaderRouterModel))
	assert.Equal(t, []string{gatewayCompactionOpusModel}, anthropicGateway.models, "the allowlisted summarizer behind the held gateway serves the turn")
	assert.Empty(t, openAIGateway.models)
}

// A compaction turn whose summarizer fails before output must be rescued onto
// another reachable model instead of surfacing the upstream error; Claude Code
// otherwise retries the same doomed compaction indefinitely.
func TestGatewayCompactionFailsOverBeforeCommit(t *testing.T) {
	svc, ctx, anthropicGateway, openAIGateway := gatewayCompactionFixture(&providers.UpstreamErrorResponse{Status: http.StatusBadGateway})

	rec, err := proxyGatewayCompaction(t, svc, ctx)

	require.NoError(t, err)
	require.NotEmpty(t, anthropicGateway.models)
	for _, model := range anthropicGateway.models {
		assert.Equal(t, gatewayCompactionOpusModel, model)
	}
	assert.Equal(t, []string{catalog.ModelGPT6Luna}, openAIGateway.models, "the generic utility model rescues the failed summarizer")
	assert.Equal(t, catalog.ModelGPT6Luna, rec.Header().Get(HeaderRouterModel))
	assert.Contains(t, rec.Body.String(), "summary")
}
