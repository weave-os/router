package conversation

import (
	"context"
	"time"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/handover"
	"weave-os/router/internal/translate"
)

type ModelSwitchSummary struct {
	Summarizer handover.Summarizer
	Access     SummarizerAccess
	Now        func() time.Time
}

func (m ModelSwitchSummary) Run(ctx context.Context, env *translate.RequestEnvelope, scope router.Request, pinModel, freshModel string) (HandoverOutcome, bool) {
	log := observability.FromContext(ctx)
	out := HandoverOutcome{Invoked: true}
	if m.Summarizer == nil {
		out.FallbackToFullHistory = true
		log.Info("Handover summarizer not wired; preserved full history instead", "pin_model", pinModel, "fresh_model", freshModel)
		return out, false
	}
	sumProvider := m.Summarizer.Provider()
	summaryCtx, allowed := m.Access.SummaryContext(ctx, sumProvider)
	if !allowed {
		out.FallbackToFullHistory = true
		log.Info("Handover summarizer skipped to preserve tenant boundary; preserved full history instead", "pin_model", pinModel, "fresh_model", freshModel, "sum_provider", sumProvider)
		return out, false
	}
	start := m.Now()
	summary, summaryUsage, sumErr := m.Summarizer.Summarize(summaryCtx, env, scope)
	out.LatencyMS = m.Now().Sub(start).Milliseconds()
	switch {
	case sumErr != nil:
		out.FallbackToFullHistory = true
		log.Warn("Handover summarizer failed; preserved full history instead", "err", sumErr, "pin_model", pinModel, "fresh_model", freshModel)
	case summary == "":
		out.FallbackToFullHistory = true
		log.Warn("Handover summarizer returned empty summary; preserved full history instead", "pin_model", pinModel, "fresh_model", freshModel)
	default:
		handover.RewriteEnvelope(env, summary)
		out.SummaryTokens = EstimateSummaryTokens(summary)
		out.SummaryUsage = summaryUsage
		return out, true
	}
	return out, false
}
