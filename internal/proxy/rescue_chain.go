package proxy

import (
	"context"
	"net/http"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"
)

type rescueServices interface {
	dispatchWithFallback(ctx context.Context, in dispatch.FailoverInputs) (winnerIdx int, err error)
	resolveEffort(ctx context.Context, decision router.Decision, caps router.ModelSpec, escalate bool) dispatch.EffortResolution
	runCompactionHandover(ctx context.Context, env *translate.RequestEnvelope, reqHeaders http.Header, decisionModel string, scope router.Request) dispatch.HandoverOutcome
	earlyStopRedoMode(ctx context.Context, routeRes dispatch.RescueTurn, decision router.Decision, streaming, agentShadow bool) flags.EarlyStopRedoMode
	redoEarlyStop(ctx context.Context, redo dispatch.EarlyStopRedo) (redone bool, err error)
}

func rescueTurnOf(res turnLoopResult) dispatch.RescueTurn {
	return dispatch.RescueTurn{
		Decision:                    res.Decision,
		Purpose:                     res.Purpose,
		Origin:                      res.Origin,
		SessionKey:                  res.SessionKey,
		InstallationID:              res.InstallationID,
		TurnType:                    res.TurnType,
		StickyHit:                   res.StickyHit,
		HardPinned:                  res.HardPinned,
		AuthoritativePerTurn:        res.AuthoritativePerTurn,
		CallerModelPassthrough:      res.CallerModelPassthrough,
		UsageBypass:                 res.UsageBypass,
		StickyRole:                  stickyStateRole(res),
		FreshMetadata:               res.Fresh.Metadata,
		PriorServedModel:            res.PriorServedModel,
		SessionEverSwitched:         res.SessionEverSwitched,
		HandoverInvoked:             res.Handover.Invoked,
		PrefixTrimmed:               res.PrefixTrimmed,
		EscalateEffort:              res.EscalateEffort,
		TranslationEnabledProviders: res.TranslationEnabledProviders,
	}
}
