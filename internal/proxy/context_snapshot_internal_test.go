package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/cache"
	"weave-os/router/internal/translate"
)

func TestContextEstimateHeadersProtocols(t *testing.T) {
	fixtures := []struct {
		name  string
		parse func([]byte) (*translate.RequestEnvelope, error)
		body  string
	}{
		{"anthropic", translate.ParseAnthropic, `{"model":"claude-sonnet-5","max_tokens":16000,"messages":[{"role":"user","content":"inspect the build failure"}]}`},
		{"openai", translate.ParseOpenAI, `{"model":"gpt-5.6-sol","max_tokens":16000,"messages":[{"role":"user","content":"inspect the build failure"}]}`},
		{"gemini", translate.ParseGemini, `{"contents":[{"role":"user","parts":[{"text":"inspect the build failure"}]}],"generationConfig":{"maxOutputTokens":16000}}`},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			env, err := fixture.parse([]byte(fixture.body))
			require.NoError(t, err)
			headers := http.Header{}
			setContextEstimateHeaders(headers, env.ContextOverflowTokenEstimate(), 16000)
			require.Equal(t, "16000", headers.Get(HeaderRouterContextReserve))
			require.Equal(t, "approximate", headers.Get(HeaderRouterContextEstimateKind))
			require.Equal(t, "1", headers.Get(HeaderRouterContextVersion))
			require.NotEmpty(t, headers.Get(HeaderRouterContextEstimate))
			require.NotEqual(t, "0", headers.Get(HeaderRouterContextEstimate))
		})
	}
}

func TestCachedContextEstimateUsesLiveRequest(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(`{"model":"claude-sonnet-5","max_tokens":32,"messages":[{"role":"user","content":"a fresh request"}]}`))
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	setContextEstimateHeaders(rec.Header(), env.ContextOverflowTokenEstimate(), 8000)
	estimate := rec.Header().Get(HeaderRouterContextEstimate)
	stale := http.Header{}
	stale.Set(HeaderRouterContextEstimate, "999999")
	stale.Set(HeaderRouterContextReserve, "1")
	stale.Set(HeaderRouterContextEstimateKind, "exact")
	stale.Set(HeaderRouterContextWindow, "1")
	(&Service{}).writeCachedResponse(rec, cache.CachedResponse{Headers: stale, Body: []byte(`{"ok":true}`)}, router.Decision{Model: "claude-sonnet-5", Provider: providers.ProviderAnthropic})
	require.Equal(t, estimate, rec.Result().Header.Get(HeaderRouterContextEstimate))
	require.Equal(t, "8000", rec.Result().Header.Get(HeaderRouterContextReserve))
	require.Equal(t, "approximate", rec.Result().Header.Get(HeaderRouterContextEstimateKind))
	require.NotEqual(t, "1", rec.Result().Header.Get(HeaderRouterContextWindow))
	require.Equal(t, "hit", rec.Result().Header.Get(HeaderRouterCache))
}

func TestContextSnapshotFreshness(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	snapshot := ContextSnapshot{Version: 1, EstimateKind: ContextEstimateApproximate, EstimateTokens: 72000, ContextWindow: 128000, OutputReserveTokens: 8000, ServedModel: "gpt-5.6-sol", RequestID: "request-test", RequestedAt: now.Add(-time.Minute), RecordedAt: now}
	require.True(t, snapshot.Fresh(now))
	require.False(t, snapshot.Fresh(now.Add(ContextSnapshotTTL+time.Second)))
	require.False(t, snapshot.Fresh(now.Add(-time.Second)))
	snapshot.EstimateKind = "exact"
	require.False(t, snapshot.Fresh(now))
	snapshot.EstimateKind = ContextEstimateApproximate
	snapshot.EstimateTokens = 0
	require.False(t, snapshot.Fresh(now))
	require.Nil(t, ParseContextSnapshot([]byte(`{"version":`)))
}

func TestContextSnapshotUsesFinalServedModel(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	headers := http.Header{}
	setContextEstimateHeaders(headers, 72000, 8000)
	headers.Set(HeaderRouterContextWindow, "1000000")
	headers.Set(HeaderRouterModel, "deepseek/deepseek-v4-pro")
	snapshot := contextSnapshotJSONForDecision(headers, "request-final", "claude-opus-4-8", "claude-opus-4-8", providers.ProviderAnthropic, now, now)
	parsed := ParseContextSnapshot(snapshot)
	require.NotNil(t, parsed)
	require.Equal(t, "claude-opus-4-8", parsed.ServedModel)
	require.Equal(t, 1_000_000, parsed.ContextWindow)
}
