package conversation

import (
	"context"
	"time"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/handover"
	"weave-os/router/internal/translate"
)

type CompactionHandover struct {
	Summarizer handover.Summarizer
	Access     SummarizerAccess
	Now        func() time.Time
}

func (h CompactionHandover) Run(ctx context.Context, env *translate.RequestEnvelope, decisionModel string, scope router.Request) HandoverOutcome {
	log := observability.FromContext(ctx)
	var out HandoverOutcome
	out.Invoked = true

	if h.Summarizer == nil {
		out.FallbackToFullHistory = true
		log.Info("Compaction handover: summarizer not wired; preserved compacted body instead", "decision_model", decisionModel)
		return out
	}
	summaryCtx, allowed := h.Access.SummaryContext(ctx, h.Summarizer.Provider())
	if !allowed {
		out.FallbackToFullHistory = true
		log.Info("Compaction handover: summarizer skipped (tenant boundary); preserved compacted body instead", "decision_model", decisionModel)
		return out
	}
	start := h.Now()
	summary, summaryUsage, sumErr := h.Summarizer.Summarize(summaryCtx, env, scope)
	out.LatencyMS = h.Now().Sub(start).Milliseconds()
	switch {
	case sumErr != nil:
		out.FallbackToFullHistory = true
		log.Warn("Compaction handover: summarizer failed; preserved compacted body instead", "err", sumErr, "decision_model", decisionModel)
	case summary == "":
		out.FallbackToFullHistory = true
		log.Warn("Compaction handover: summarizer returned empty; preserved compacted body instead", "decision_model", decisionModel)
	default:
		handover.RewriteEnvelope(env, summary)
		out.SummaryTokens = EstimateSummaryTokens(summary)
		out.SummaryUsage = summaryUsage
		log.Info("Compaction handover: context rewritten with summary", "summary_len", len(summary), "decision_model", decisionModel)
	}
	return out
}
