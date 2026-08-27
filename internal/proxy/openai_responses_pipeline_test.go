package proxy_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"workweave/router/internal/billing"
	"workweave/router/internal/providers"
	"workweave/router/internal/proxy"
	"workweave/router/internal/router"
	"workweave/router/internal/router/cache"
	"workweave/router/internal/router/catalog"
	"workweave/router/internal/router/handover"
	"workweave/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// responsesTextUpstream answers a text-only Responses stream whose usage
// reports a partially cached prefix.
func responsesTextUpstream(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, frame := range []string{
		`{"type":"response.created","response":{"id":"resp_1","status":"in_progress"}}`,
		`{"type":"response.output_text.delta","output_index":0,"delta":"cached answer"}`,
		`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"cached answer"}]}],"usage":{"input_tokens":40,"output_tokens":6,"input_tokens_details":{"cached_tokens":32}}}}`,
	} {
		_, _ = io.WriteString(w, "data: "+frame+"\n\n")
	}
}

const chatCacheableTurnBody = `{"model":"auto","stream":false,"max_tokens":256,
  "messages":[{"role":"user","content":"summarize the release notes"}],
  "tools":[{"type":"function","function":{"name":"noop","parameters":{"type":"object"}}}],
  "reasoning_effort":"medium"}`

func openAIChatServiceWithDecision(provider providers.Client, decision router.Decision, c *cache.Cache) *proxy.Service {
	return proxy.NewService(
		&fakeRouter{decision: decision},
		map[string]providers.Client{providers.ProviderOpenAI: provider},
		nil, false, c, nil, false, providers.ProviderOpenAI, "gpt-5.6-sol", nil,
	)
}

// The semantic cache stores what the CLIENT received, so a turn served over
// Responses must be replayed as chat/completions — caching the upstream's
// Responses body would hand the next caller a foreign wire format.
func TestService_ProxyOpenAIChatCompletion_ResponsesTurnCachesTranslatedBody(t *testing.T) {
	provider := &fakeProvider{proxyResponse: responsesTextUpstream}
	decision := router.Decision{
		Provider: providers.ProviderOpenAI,
		Model:    "gpt-5.6-luna",
		Reason:   "test",
		Metadata: &router.RoutingMetadata{Embedding: embeddingFixture(11), ClusterIDs: []int{0, 1}},
	}
	svc := openAIChatServiceWithDecision(provider, decision, cache.New(cache.DefaultConfig()))
	ctx := proxyContextWithExternalID(t, "tenant-responses")

	rec1 := httptest.NewRecorder()
	require.NoError(t, svc.ProxyOpenAIChatCompletion(ctx, []byte(chatCacheableTurnBody), rec1,
		httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatCacheableTurnBody))))
	require.Len(t, provider.proxyEndpoints, 1)
	require.Equal(t, providers.EndpointResponses, provider.proxyEndpoints[0])

	rec2 := httptest.NewRecorder()
	require.NoError(t, svc.ProxyOpenAIChatCompletion(ctx, []byte(chatCacheableTurnBody), rec2,
		httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatCacheableTurnBody))))

	assert.Len(t, provider.proxyEndpoints, 1, "the second turn must be served from cache")
	assert.Equal(t, proxy.RouterCacheHit, rec2.Header().Get(proxy.HeaderRouterCache))
	replayed := rec2.Body.Bytes()
	assert.Equal(t, "chat.completion", gjson.GetBytes(replayed, "object").String(),
		"a cached Responses turn must replay as chat/completions")
	assert.Equal(t, "cached answer", gjson.GetBytes(replayed, "choices.0.message.content").String())
	assert.Equal(t, rec1.Body.String(), rec2.Body.String())
}

// Usage lives in the Responses payload, not in a chat.completion, so the ledger
// debit is only correct if the translated usage — including the cached prefix —
// feeds billing.
func TestService_ProxyOpenAIChatCompletion_ResponsesUsageDebitsBilling(t *testing.T) {
	repo := &capturingBillingRepo{}
	provider := &fakeProvider{proxyResponse: responsesTextUpstream}
	svc := openAIChatService(provider, "gpt-5.6-luna").
		WithBillingService(billing.NewService(repo))

	ctx := proxyContextWithExternalID(t, "tenant-billing")
	rec := httptest.NewRecorder()
	require.NoError(t, svc.ProxyOpenAIChatCompletion(ctx, []byte(chatCacheableTurnBody), rec,
		httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatCacheableTurnBody))))
	require.Equal(t, providers.EndpointResponses, provider.proxyEndpoints[0])

	price, ok := catalog.PriceFor(providers.ProviderOpenAI, "gpt-5.6-luna")
	require.True(t, ok)
	want := catalog.USDToMicros(
		catalog.EffectiveInputCost(40, 0, 32, price.InputUSDPer1M, price, providers.ProviderOpenAI) +
			catalog.EffectiveOutputCost(6, price.OutputUSDPer1M))

	debits := repo.recordedDebits()
	require.Len(t, debits, 1, "a served Responses turn must debit exactly once")
	assert.Equal(t, "gpt-5.6-luna", debits[0].RouterModel)
	assert.Equal(t, want, debits[0].NotionalCostMicros,
		"the debit must price the Responses usage block, cached prefix included")
	assert.Positive(t, want)
}

type fakeChatCompactionSummarizer struct {
	summary string
	calls   int
}

func (f *fakeChatCompactionSummarizer) SummarizeForCompaction(context.Context, *translate.RequestEnvelope, string, int) (string, handover.Usage, error) {
	f.calls++
	return f.summary, handover.Usage{InputTokens: 10, OutputTokens: 4}, nil
}

func (f *fakeChatCompactionSummarizer) Provider() string { return providers.ProviderAnthropic }

// Compaction rewrites the envelope before emit, so a compacted chat turn must
// still be emitted as Responses and carry the summary — the rewritten history,
// never the original messages.
func TestService_ProxyOpenAIChatCompletion_CompactedTurnStillEmitsResponses(t *testing.T) {
	summarizer := &fakeChatCompactionSummarizer{summary: "COMPACTED HISTORY SUMMARY"}
	provider := &fakeProvider{proxyResponse: responsesTextUpstream}
	svc := openAIChatService(provider, "gpt-5.6-luna").
		// gpt-4o's 128K window is the largest eligible one here, so a history
		// past it forces the cascade all the way to summarization.
		WithAvailableModels(map[string]struct{}{"gpt-4o": {}}).
		WithCompaction(summarizer, proxy.DefaultCompactionTriggerPct)

	var sb strings.Builder
	sb.WriteString(`{"model":"auto","stream":false,"max_tokens":256,"messages":[`)
	for i := range 400 {
		if i > 0 {
			sb.WriteString(",")
		}
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		sb.WriteString(`{"role":"` + role + `","content":"` + strings.Repeat("history ", 200) + `"}`)
	}
	sb.WriteString(`],"tools":[{"type":"function","function":{"name":"read_file","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}}],"reasoning_effort":"medium"}`)
	body := sb.String()

	rec := httptest.NewRecorder()
	require.NoError(t, svc.ProxyOpenAIChatCompletion(context.Background(), []byte(body), rec,
		httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))))

	require.Positive(t, summarizer.calls, "the oversized turn must be compacted")
	require.Len(t, provider.proxyBodies, 1)
	assert.Equal(t, providers.EndpointResponses, provider.proxyEndpoints[0],
		"a rewritten envelope must still be emitted onto Responses")
	sent := provider.proxyBodies[0]
	assert.False(t, gjson.GetBytes(sent, "messages").Exists())
	assert.Contains(t, string(sent), "COMPACTED HISTORY SUMMARY")
	assert.Equal(t, "chat.completion", gjson.GetBytes(rec.Body.Bytes(), "object").String())
}
