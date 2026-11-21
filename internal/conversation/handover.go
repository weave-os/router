package conversation

import (
	"context"

	"weave-os/router/internal/router/handover"
)

type HandoverOutcome struct {
	Invoked               bool
	LatencyMS             int64
	SummaryTokens         int
	FallbackToFullHistory bool
	SummaryUsage          handover.Usage
}

type SummarizerAccess interface {
	SummaryContext(ctx context.Context, provider string) (summaryCtx context.Context, allowed bool)
}

func EstimateSummaryTokens(s string) int {
	return len(s) / 4
}
