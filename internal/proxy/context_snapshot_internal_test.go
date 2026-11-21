package proxy

import (
	"net/http"
	"testing"
	"time"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/require"
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
			require.Equal(t, "16000", headers.Get(requestcontext.HeaderRouterContextReserve))
			require.Equal(t, "approximate", headers.Get(requestcontext.HeaderRouterContextEstimateKind))
			require.Equal(t, "1", headers.Get(requestcontext.HeaderRouterContextVersion))
			require.NotEmpty(t, headers.Get(requestcontext.HeaderRouterContextEstimate))
			require.NotEqual(t, "0", headers.Get(requestcontext.HeaderRouterContextEstimate))
		})
	}
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
	headers.Set(requestcontext.HeaderRouterContextWindow, "1048576")
	headers.Set(requestcontext.HeaderRouterModel, "deepseek/deepseek-v4-pro")
	snapshot := contextSnapshotJSONForDecision(headers, "request-final", "claude-opus-4-8", "claude-opus-4-8", providers.ProviderAnthropic, now, now)
	parsed := ParseContextSnapshot(snapshot)
	require.NotNil(t, parsed)
	require.Equal(t, "claude-opus-4-8", parsed.ServedModel)
	require.Equal(t, 1_000_000, parsed.ContextWindow)
}
