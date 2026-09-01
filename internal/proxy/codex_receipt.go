package proxy

import (
	"fmt"
	"strings"

	"workweave/router/internal/router/catalog"
	"workweave/router/internal/translate"
)

// Codex renders a turn's own token counts from what it asked for, never from
// what the router served, so a routed turn's real cost is invisible in the
// client: the footer keeps showing the requested model. The receipt closes that
// gap by reporting the served turn's tokens and what routing saved against the
// model the client would otherwise have paid for.
const (
	// Marks the receipt so the next inbound turn strips it back out — the same
	// contract the routing badge and feedback footer rely on. Without it the
	// receipt accumulates in the prompt history it is describing.
	codexReceiptPrefix = "\n\n↳ Weave Router · "

	// Below this the "saved" clause is dropped rather than rounded to $0.00,
	// which reads as "routing bought you nothing" instead of "the win was
	// smaller than a cent".
	codexReceiptMinSavingsUSD = 0.005
)

// codexReceiptRenderer returns a renderer for the per-turn receipt, or nil when
// this turn should not carry one.
//
// requestedPricing is the client's own model, actualPricing the model that
// served. Passing the same pricing for both is not a bug to guard against here
// — it yields no savings clause, which is the correct rendering for a turn the
// router left on the requested model.
func codexReceiptRenderer(
	requestedPricing catalog.Pricing,
	actualPricing catalog.Pricing,
	provider string,
) func(translate.ResponsesReceiptUsage) string {
	return func(usage translate.ResponsesReceiptUsage) string {
		// An upstream that reported no usage gives us nothing to render, and a
		// receipt reading "0 in / 0 out" would be a lie about a turn that did
		// consume tokens.
		if !usage.HasUsage || (usage.InputTokens <= 0 && usage.OutputTokens <= 0) {
			return ""
		}

		var b strings.Builder
		b.WriteString(codexReceiptPrefix)
		fmt.Fprintf(&b, "%s in / %s out",
			formatReceiptTokens(usage.InputTokens),
			formatReceiptTokens(usage.OutputTokens),
		)

		if clause := receiptSavingsClause(
			requestedPricing, actualPricing, provider,
			usage.InputTokens, usage.OutputTokens, usage.CacheReadTokens,
		); clause != "" {
			b.WriteString(clause)
		}

		return b.String()
	}
}

// receiptSavingsClause renders the savings for one turn, or "" when there is
// nothing worth claiming.
//
// Both sides price the SAME served token counts: the comparison is "what this
// turn's work would have cost on the requested model", not a guess at how many
// tokens that model would have used. Cache tokens flow through
// EffectiveInputCost so a cache-heavy turn is not credited with savings it did
// not earn.
func receiptSavingsClause(
	requestedPricing catalog.Pricing,
	actualPricing catalog.Pricing,
	provider string,
	inputTokens int64,
	outputTokens int64,
	cacheRead int64,
) string {
	in, out := int(inputTokens), int(outputTokens)
	// EffectiveInputCost takes the TOTAL prompt and nets out the cached portion
	// itself, so pass the upstream's numbers through unmodified. Pre-subtracting
	// here would discount the cached tokens twice.
	read := min(int(cacheRead), in)

	requested := catalog.EffectiveInputCost(in, 0, read, requestedPricing.InputUSDPer1M, requestedPricing, provider) +
		catalog.EffectiveOutputCost(out, requestedPricing.OutputUSDPer1M)
	actual := catalog.EffectiveInputCost(in, 0, read, actualPricing.InputUSDPer1M, actualPricing, provider) +
		catalog.EffectiveOutputCost(out, actualPricing.OutputUSDPer1M)

	// A negative delta means routing deliberately bought a pricier model for
	// quality. Reporting "saved -$0.02" invites the reader to conclude the
	// router malfunctioned, so the clause is omitted instead.
	saved := requested - actual
	if saved < codexReceiptMinSavingsUSD {
		return ""
	}
	return fmt.Sprintf(" · saved $%.2f", saved)
}

// formatReceiptTokens renders a token count compactly enough to sit in one
// status line: exact below 1k, then one decimal place.
func formatReceiptTokens(tokens int64) string {
	if tokens < 0 {
		return "0"
	}
	if tokens < 1000 {
		return fmt.Sprintf("%d", tokens)
	}
	return fmt.Sprintf("%.1fk", float64(tokens)/1000)
}
