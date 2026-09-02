package proxy

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"workweave/router/internal/providers"
	"workweave/router/internal/router/catalog"
	"workweave/router/internal/router/turntype"
	"workweave/router/internal/translate"
)

// A mid-tier requested model against a cheap served one: the shape a routed
// Codex turn actually takes.
var (
	receiptRequestedPricing = catalog.Pricing{InputUSDPer1M: 15, OutputUSDPer1M: 75}
	receiptActualPricing    = catalog.Pricing{InputUSDPer1M: 3, OutputUSDPer1M: 15}
)

func testReceiptPricing(actual catalog.Pricing, provider string) *codexReceiptPricing {
	return &codexReceiptPricing{actualPricing: actual, provider: provider, hasActualPricing: true}
}

func TestCodexReceiptPricingTracksActualBinding(t *testing.T) {
	require.NotEmpty(t, catalog.Models)
	model := catalog.Models[0]
	require.NotEmpty(t, model.Providers)
	binding := model.Providers[0]

	pricing := &codexReceiptPricing{}
	pricing.setActual(binding.Provider, model.ID)
	assert.True(t, pricing.hasActualPricing)
	assert.Equal(t, binding.Provider, pricing.provider)
	assert.Equal(t, binding.Price, pricing.actualPricing)

	pricing.setActual(binding.Provider, model.ID+"-missing")
	assert.False(t, pricing.hasActualPricing)
}

func TestCodexReceiptTurnGating(t *testing.T) {
	ctx := context.Background()
	assert.True(t, codexReceiptTurn(ctx, ClientAppCodex, turntype.MainLoop))
	assert.True(t, codexReceiptTurn(ctx, ClientAppCodex, turntype.ToolResult))
	assert.False(t, codexReceiptTurn(ctx, ClientAppCodex, turntype.SubAgentDispatch))
	assert.False(t, codexReceiptTurn(ctx, ClientAppClaudeCode, turntype.MainLoop))

	hiddenCtx := context.WithValue(ctx, InstallationHideTerminalSurfacesContextKey{}, true)
	assert.False(t, codexReceiptTurn(hiddenCtx, ClientAppCodex, turntype.MainLoop))
}

func TestCodexReceiptReportsTokensAndSavings(t *testing.T) {
	render := codexReceiptRenderer(receiptRequestedPricing, testReceiptPricing(receiptActualPricing, providers.ProviderAnthropic))

	got := render(translate.ResponsesReceiptUsage{
		InputTokens:  1800,
		OutputTokens: 412,
		HasUsage:     true,
	})

	// 1800 in: (15-3)/1M * 1800 = $0.0216; 412 out: (75-15)/1M * 412 = $0.0247.
	require.Equal(t, "\n\n↳ Weave Router · 1.8k in / 412 out · saved $0.05", got)
}

func TestCodexReceiptOmittedWhenUpstreamReportedNoUsage(t *testing.T) {
	render := codexReceiptRenderer(receiptRequestedPricing, testReceiptPricing(receiptActualPricing, providers.ProviderAnthropic))

	assert.Empty(t, render(translate.ResponsesReceiptUsage{HasUsage: false}),
		"a turn with no upstream usage must not render a receipt")
	assert.Empty(t, render(translate.ResponsesReceiptUsage{HasUsage: true}),
		"zeroed usage is indistinguishable from missing usage and must not render")
}

func TestCodexReceiptDropsSavingsWhenRoutedModelCostsMore(t *testing.T) {
	// Routing up-tiered the turn for quality. "saved -$0.02" reads as a bug.
	render := codexReceiptRenderer(receiptActualPricing, testReceiptPricing(receiptRequestedPricing, providers.ProviderAnthropic))

	got := render(translate.ResponsesReceiptUsage{
		InputTokens:  1800,
		OutputTokens: 412,
		HasUsage:     true,
	})

	require.Equal(t, "\n\n↳ Weave Router · 1.8k in / 412 out", got)
}

func TestCodexReceiptDropsSubCentSavings(t *testing.T) {
	// A tiny turn's win rounds to $0.00, which reads as "routing bought
	// nothing" rather than "the win was small".
	render := codexReceiptRenderer(receiptRequestedPricing, testReceiptPricing(receiptActualPricing, providers.ProviderAnthropic))

	got := render(translate.ResponsesReceiptUsage{
		InputTokens:  10,
		OutputTokens: 5,
		HasUsage:     true,
	})

	require.Equal(t, "\n\n↳ Weave Router · 10 in / 5 out", got)
}

func TestCodexReceiptNoSavingsClauseWhenServedRequestedModel(t *testing.T) {
	// The router stayed on the client's model, so there is no counterfactual.
	render := codexReceiptRenderer(receiptRequestedPricing, testReceiptPricing(receiptRequestedPricing, providers.ProviderAnthropic))

	got := render(translate.ResponsesReceiptUsage{
		InputTokens:  50_000,
		OutputTokens: 2_000,
		HasUsage:     true,
	})

	require.Equal(t, "\n\n↳ Weave Router · 50.0k in / 2.0k out", got)
}

func TestCodexReceiptCacheReadsDoNotInflateSavings(t *testing.T) {
	// Cached input is billed at the cache rate on BOTH sides, so a cache-heavy
	// turn must not be credited with the full-price delta. OpenAI is the
	// provider the Responses path actually serves, and the one whose usage
	// reports cached tokens as a subset of the prompt.
	render := codexReceiptRenderer(receiptRequestedPricing, testReceiptPricing(receiptActualPricing, providers.ProviderOpenAI))

	uncached := render(translate.ResponsesReceiptUsage{
		InputTokens: 40_000, OutputTokens: 500, HasUsage: true,
	})
	cached := render(translate.ResponsesReceiptUsage{
		InputTokens: 40_000, OutputTokens: 500, CacheReadTokens: 40_000, HasUsage: true,
	})

	require.NotEqual(t, uncached, cached,
		"cache-read tokens must change the savings the receipt claims")
	assert.Equal(t, "\n\n↳ Weave Router · 40.0k in / 500 out · saved $0.51", uncached)
	// Cached input is billed at DefaultCacheReadMultiplier (0.5) on both sides,
	// so the input half of the delta is halved: $0.51 -> $0.27.
	assert.Equal(t, "\n\n↳ Weave Router · 40.0k in / 500 out · saved $0.27", cached)
}

func TestFormatReceiptTokens(t *testing.T) {
	for _, tc := range []struct {
		tokens int64
		want   string
	}{
		{tokens: 0, want: "0"},
		{tokens: 999, want: "999"},
		{tokens: 1000, want: "1.0k"},
		{tokens: 1849, want: "1.8k"},
		{tokens: 128_000, want: "128.0k"},
		{tokens: -5, want: "0"},
	} {
		assert.Equal(t, tc.want, formatReceiptTokens(tc.tokens), "tokens=%d", tc.tokens)
	}
}
