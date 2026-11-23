package routingpolicy

import (
	"context"
	"sort"
	"time"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/candidates"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/sessionpin"
)

func ServeForceModelPinTurn(ctx context.Context, req *router.Request, res *TurnRoute, forceModelPin, forceHistory sessionpin.Pin, sessionPins SessionPins, now time.Time) (served bool, tierFloor catalog.Tier) {
	log := observability.FromContext(ctx)
	verdict := candidates.JudgeForceModelPin(forceModelPin, *req)
	if verdict.PastContextEstimate {
		log.Info("Forced session pin dispatched past the context-window estimate; the provider decides", "pin_model", forceModelPin.Model, "pin_provider", forceModelPin.Provider, "role", res.PinRole)
	}
	if verdict.PastImageCap {
		log.Info("Forced session pin dispatched past the image-count cap; the provider decides", "pin_model", forceModelPin.Model, "pin_provider", forceModelPin.Provider, "role", res.PinRole)
	}
	if verdict.Serve {
		res.PinModel = forceModelPin.Model
		res.PinAgeSec = PinAge(forceModelPin, now)
		res.EscalateEffort = !forceHistory.LastTurnEndedAt.IsZero() &&
			(forceHistory.LastOutputTokens == 0 || forceHistory.ConsecutiveUpstreamErrors > 0)
		res.ServeForceModelPin(forceModelPin)
		sessionPins.AnchorForceModelHistory(ctx, res.InstallationID, res.SessionKey, res.PinRole, forceModelPin)
		return true, catalog.TierUnknown
	}
	res.ForcedPinDropped = true
	res.ForcedPinDropReason = verdict.DropReason
	res.ForcedPinModel = forceModelPin.Model
	log.Info("Forced session pin dropped for this turn", "pin_model", forceModelPin.Model, "pin_provider", forceModelPin.Provider, "drop_reason", res.ForcedPinDropReason, "enabled_providers", SortedEnabledKeys(req.EnabledProviders), "role", res.PinRole)
	if verdict.ExcludeModel {
		req.Candidates.ExcludedModels = candidates.ExcludingModel(req.Candidates.ExcludedModels, forceModelPin.Model)
	}
	return false, verdict.TierFloor
}

func ServeUserForcedPinTurn(ctx context.Context, req *router.Request, res *TurnRoute, pin sessionpin.Pin, sessionPins SessionPins) (served bool, tierFloor catalog.Tier) {
	verdict := candidates.JudgeUserForcedPin(pin, *req)
	if verdict.Serve {
		decision := pin.Decision()
		res.PinTier = pin.Reason
		res.Decision = decision
		res.UserForced = decision.UserForcedModel()
		res.StickyHit = true
		sessionPins.Refresh(ctx, res.InstallationID, res.SessionKey, pin, res.PinRole, decision)
		return true, catalog.TierUnknown
	}
	observability.FromContext(ctx).Info("Forced session pin dropped for this turn", "pin_model", pin.Model, "pin_provider", pin.Provider, "pin_reason", pin.Reason, "drop_reason", verdict.DropReason, "enabled_providers", SortedEnabledKeys(req.EnabledProviders), "role", res.PinRole)
	res.ForcedPinDropped = true
	res.ForcedPinDropReason = verdict.DropReason
	res.ForcedPinModel = pin.Model
	if verdict.ExcludeModel {
		req.Candidates.ExcludedModels = candidates.ExcludingModel(req.Candidates.ExcludedModels, pin.Model)
	}
	return false, verdict.TierFloor
}

func SortedEnabledKeys(m map[string]struct{}) []string {
	if len(m) == 0 {
		return []string{}
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
