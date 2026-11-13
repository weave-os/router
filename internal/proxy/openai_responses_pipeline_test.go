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
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/sessionpin"

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

const chatCacheableTurnBody = `{"model":"auto","stream":false,"max_tokens":4096,
  "messages":[{"role":"user","content":"summarize the release notes"}],
  "reasoning_effort":"medium"}`

func openAIChatServiceWithDecision(provider providers.Client, decision router.Decision) *proxy.Service {
	return proxy.NewService(
		&fakeRouter{decision: decision},
		map[string]providers.Client{providers.ProviderOpenAI: provider},
		nil, nil, false, providers.ProviderOpenAI, "gpt-5.6-sol", nil,
	)
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
		catalog.EffectiveInputCost(40, 0, 32, price, providers.ProviderOpenAI, catalog.UsageModifiers{}) +
			catalog.EffectiveOutputCost(40, 6, price, catalog.UsageModifiers{}))

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
		nil, store, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil,
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

func TestService_ProxyOpenAIChatCompletion_HandoverUsesResponsesEndpoint(t *testing.T) {
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
		nil, store, false, providers.ProviderOpenAI, "gpt-5.6-sol", nil,
	).WithSummarizer(summarizer)
	body := `{"model":"auto","stream":true,"max_tokens":256,"messages":[{"role":"user","content":"` + strings.Repeat("aaaa ", 8000) + `"}],"tools":[{"type":"function","function":{"name":"noop","parameters":{"type":"object"}}}],"reasoning_effort":"medium"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))

	require.NoError(t, svc.ProxyOpenAIChatCompletion(authedCtx(uuid.New().String()), []byte(body), rec, req))
	require.Equal(t, int32(1), summarizer.calls.Load(), "the switch must invoke the summarizer")
	require.Len(t, openAI.proxyEndpoints, 1)
	assert.Equal(t, providers.EndpointResponses, openAI.proxyEndpoints[0])
	assert.Contains(t, string(openAI.proxyBodies[0]), "HANDOVER SUMMARY MARKER")
}

func proxyContextWithExternalID(t *testing.T, externalID string) context.Context {
	t.Helper()
	ctx := context.WithValue(context.Background(), proxy.APIKeyIDContextKey{}, "pipeline-test-key")
	if externalID != "" {
		ctx = context.WithValue(ctx, proxy.ExternalIDContextKey{}, externalID)
	}
	return ctx
}
