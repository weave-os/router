package dispatch

import "weave-os/router/internal/router/handover"

type HandoverOutcome struct {
	Invoked               bool
	LatencyMS             int64
	SummaryTokens         int
	FallbackToFullHistory bool
	SummaryUsage          handover.Usage
}
