package dispatch

import "weave-os/router/internal/router/handover"

// HandoverOutcome describes the synchronous handover step.
type HandoverOutcome struct {
	Invoked       bool
	LatencyMS     int64
	SummaryTokens int
	// FallbackToFullHistory is set when handover was invoked but no summary
	// was applied (unwired, tenant-boundary skip, timeout, error, or empty
	// summary), so the full body passes through unchanged. No ledger row billed.
	FallbackToFullHistory bool
	// SummaryUsage is the summarizer call's upstream usage, so fireBilling can
	// debit it as a separate "_summary" ledger row. Zero on fallback/error paths.
	SummaryUsage handover.Usage
}
