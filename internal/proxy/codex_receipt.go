package proxy

import (
	"fmt"
	"strings"

	"workweave/router/internal/router/catalog"
	"workweave/router/internal/router/turntype"
	"workweave/router/internal/translate"
)

const (
	codexReceiptPrefix        = "\n\n↳ Weave Router · "
	codexReceiptMinSavingsUSD = 0.005
)

type codexReceiptPricing struct {
	actualPricing    catalog.Pricing
	provider         string
	hasActualPricing bool
}

func codexReceiptTurn(clientApp string, tt turntype.TurnType) bool {
	return clientApp == ClientAppCodex && (tt == turntype.MainLoop || tt == turntype.ToolResult)
}

func codexReceiptRenderer(
	requestedPricing catalog.Pricing,
	pricing *codexReceiptPricing,
) func(translate.ResponsesReceiptUsage) string {
	return func(usage translate.ResponsesReceiptUsage) string {
		if !usage.HasUsage || (usage.InputTokens <= 0 && usage.OutputTokens <= 0) {
			return ""
		}

		var b strings.Builder
		b.WriteString(codexReceiptPrefix)
		fmt.Fprintf(&b, "%s in / %s out",
			formatReceiptTokens(usage.InputTokens),
			formatReceiptTokens(usage.OutputTokens),
		)

		if pricing.hasActualPricing {
			if clause := receiptSavingsClause(
				requestedPricing, pricing.actualPricing, pricing.provider,
				usage.InputTokens, usage.OutputTokens, usage.CacheReadTokens,
			); clause != "" {
				b.WriteString(clause)
			}
		}

		return b.String()
	}
}

// receiptSavingsClause renders the savings for one turn, or "" when there is
// nothing worth claiming.
func receiptSavingsClause(
	requestedPricing catalog.Pricing,
	actualPricing catalog.Pricing,
	provider string,
	inputTokens int64,
	outputTokens int64,
	cacheRead int64,
) string {
	in, out := int(inputTokens), int(outputTokens)
	read := min(int(cacheRead), in)

	requested := catalog.EffectiveInputCost(in, 0, read, requestedPricing.InputUSDPer1M, requestedPricing, provider) +
		catalog.EffectiveOutputCost(out, requestedPricing.OutputUSDPer1M)
	actual := catalog.EffectiveInputCost(in, 0, read, actualPricing.InputUSDPer1M, actualPricing, provider) +
		catalog.EffectiveOutputCost(out, actualPricing.OutputUSDPer1M)

	saved := requested - actual
	if saved < codexReceiptMinSavingsUSD {
		return ""
	}
	return fmt.Sprintf(" · saved $%.2f", saved)
}

func formatReceiptTokens(tokens int64) string {
	if tokens < 0 {
		return "0"
	}
	if tokens < 1000 {
		return fmt.Sprintf("%d", tokens)
	}
	return fmt.Sprintf("%.1fk", float64(tokens)/1000)
}
