package conversation

import (
	"context"
)

type SummarizerAccess interface {
	SummaryContext(ctx context.Context, provider string) (summaryCtx context.Context, allowed bool)
}

func EstimateSummaryTokens(s string) int {
	return len(s) / 4
}
