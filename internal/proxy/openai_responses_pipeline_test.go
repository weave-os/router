package proxy_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/cache"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/handover"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
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

// Streaming bypasses the semantic cache — its translated bytes are SSE frames
// that would be unreadable as a chat.completion body on the next hit.
func TestService_ProxyOpenAIChatCompletion_ResponsesStreamBypassesCache(t *testing.T) {
	streamingBody := strings.Replace(chatCacheableTurnBody, `"stream":false`, `"stream":true`, 1)
	provider := &fakeProvider{proxyResponse: responsesTextUpstream}
	decision := router.Decision{
		Provider: providers.ProviderOpenAI,
		Model:    "gpt-5.6-luna",
		Reason:   "test",
		Metadata: &router.RoutingMetadata{Embedding: embeddingFixture(11), ClusterIDs: []int{0, 1}},
	}
	svc := openAIChatServiceWithDecision(provider, decision, cache.New(cache.DefaultConfig()))
	ctx := proxyContextWithExternalID(t, "tenant-responses-stream")

	for range 2 {
		rec := httptest.NewRecorder()
		require.NoError(t, svc.ProxyOpenAIChatCompletion(ctx, []byte(streamingBody), rec,
			httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(streamingBody))))
		assert.NotEqual(t, proxy.RouterCacheHit, rec.Header().Get(proxy.HeaderRouterCache))
		assert.Contains(t, rec.Body.String(), "chat.completion.chunk")
	}
	assert.Equal(t,
		[]providers.Endpoint{providers.EndpointResponses, providers.EndpointResponses},
		provider.proxyEndpoints,
		"both streaming turns must reach the provider on Responses")
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

// A handover rewrites the envelope before emit, so the switch turn's Responses
// request must carry the summary rather than the original history — the same
// hazard as compaction, on the path that actually switches provider families.
func TestService_ProxyMessages_HandoverSwitchToOpenAIEmitsResponses(t *testing.T) {
	store := newFakePinStore()
	store.hasPin = true
	store.pin = sessionpin.Pin{
		Provider:        providers.ProviderAnthropic,
		Model:           "claude-opus-4-7",
		Reason:          "cluster:v0.2",
		PinnedUntil:     time.Now().Add(time.Hour),
		LastInputTokens: 5000,
		LastTurnEndedAt: time.Now().Add(-30 * time.Second),
	}
	openAI := &fakeProvider{proxyResponse: responsesTextUpstream}
	fr := &fakeRouter{decision: router.Decision{
		Provider: providers.ProviderOpenAI, Model: "gpt-5.6-luna", Reason: "cluster:v0.2",
	}}
	summarizer := &fakeSummarizer{summary: "HANDOVER SUMMARY MARKER"}
	svc := proxy.NewService(
		fr,
		map[string]providers.Client{
			providers.ProviderAnthropic: &fakeProvider{},
			providers.ProviderOpenAI:    openAI,
		},
		nil, false, nil, store, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil,
	).WithSummarizer(summarizer)

	rec := httptest.NewRecorder()
	require.NoError(t, svc.ProxyMessages(authedCtx(uuid.New().String()), largeBody(t), rec,
		httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))))

	require.Equal(t, int32(1), summarizer.calls.Load(), "the switch must invoke the summarizer")
	require.Len(t, openAI.proxyEndpoints, 1)
	assert.Equal(t, providers.EndpointResponses, openAI.proxyEndpoints[0],
		"a handover-rewritten turn to direct OpenAI must still dispatch on Responses")
	sent := string(openAI.proxyBodies[0])
	assert.Contains(t, sent, "HANDOVER SUMMARY MARKER", "the rewritten envelope must reach the upstream")
	assert.Equal(t, "cached answer", gjson.GetBytes(rec.Body.Bytes(), "content.0.text").String(),
		"the client must still get an Anthropic message")
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
