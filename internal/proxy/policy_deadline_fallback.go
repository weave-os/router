package proxy

import (
	"context"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/candidates"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/routingpolicy"
)

func servePolicyDeadlineFallback(ctx context.Context, s *Service, res *turnLoopResult, err error, req router.Request, sessionPins routingpolicy.SessionPins, pin *sessionpin.Pin) bool {
	log := observability.FromContext(ctx)
	if pin != nil && pin.Model != "" {
		decision := pin.Decision()
		decision.Reason = policyDeadlineFallbackReason
		res.Decision = decision
		res.StickyHit = true
		res.PinTier = policyDeadlineFallbackReason
		res.PolicyFallback = true
		log.Warn("policy sidecar missed its deadline; serving session pin",
			"err", err,
			"pin_model", pin.Model,
			"pin_provider", pin.Provider,
			"pin_policy_group", s.strategyPorts.Facts.PinStateOf(*pin).PolicyGroup,
			"requested_model", req.RequestedModel,
		)
		refreshed := decision
		refreshed.Reason = pin.Reason
		sessionPins.Refresh(ctx, res.InstallationID, res.SessionKey, *pin, res.PinRole, refreshed)
		return true
	}
	if decision, ok := candidates.PolicyDeadlineDefault(s.policyDeadlineDefaultModel, req, s.engine.Clients().Names()); ok {
		res.Decision = decision
		res.PinTier = policyDeadlineFallbackReason
		res.PolicyFallback = true
		log.Warn("policy sidecar missed its deadline; no session pin, serving tier-3 default",
			"err", err,
			"default_model", decision.Model,
			"default_provider", decision.Provider,
			"requested_model", req.RequestedModel,
		)
		sessionPins.WriteNew(ctx, res.InstallationID, res.SessionKey, res.PinRole, decision)
		return true
	}
	return false
}

func pinIfFound(pin sessionpin.Pin, found bool) *sessionpin.Pin {
	if !found {
		return nil
	}
	return &pin
}
