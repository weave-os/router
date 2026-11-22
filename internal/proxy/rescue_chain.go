package proxy

import (
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/routingpolicy"
)

func rescueTurnOf(res routingpolicy.TurnRoute) dispatch.RescueTurn {
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
