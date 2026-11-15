package dispatch

import "weave-os/router/internal/router/handover"

// HandoverOutcome is what the synchronous compaction handover reports to the
// turn: a fallback to full history bills nothing, and a summary bills its own
// usage as a separate ledger row.
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
