package routingpolicy

import (
	"context"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/continuity"
)

func (t SessionTurn) SettleFresh(ctx context.Context, res *TurnRoute) (TurnRoute, error) {
	if served, ok := t.reanchor(ctx, res); ok {
		return served, nil
	}
	if !t.Settings.PlannerEnabled {
		res.Decision = t.Fresh
		t.Pins.WriteNew(ctx, t.InstallationID, res.SessionKey, res.PinRole, t.Fresh)
		return *res, nil
	}
	return t.plan(ctx, res), nil
}

func (t SessionTurn) reanchor(ctx context.Context, res *TurnRoute) (TurnRoute, bool) {
	pin, fresh := t.Pin, t.Fresh
	if t.PinFound || pin.Model == "" || t.PrefixBroken {
		return TurnRoute{}, false
	}
	log := observability.FromContext(ctx)
	switch continuity.JudgeReanchor(pin, fresh, t.Request, t.Settings.AvailableModels) {
	case continuity.ReanchorNone:
	case continuity.ReanchorSkipMaxedOut:
		log.Info("Expired session pin maxed out on previous turn; skipping re-anchor", "pin_model", pin.Model, "pin_provider", pin.Provider, "last_output_tokens", pin.LastOutputTokens)
	case continuity.ReanchorSkipTextOnly:
		log.Info("Expired session pin is text-only for image-bearing turn; skipping re-anchor", "pin_model", pin.Model, "pin_provider", pin.Provider)
	case continuity.ReanchorServe:
		priorDecision := pin.Decision()
		res.Decision = priorDecision
		res.StickyHit = true
		res.PinTier = "postgres_reanchor"
		t.Pins.WriteNew(ctx, t.InstallationID, res.SessionKey, res.PinRole, priorDecision)
		log.Info("router re-anchored expired session pin", "prior_model", pin.Model, "prior_provider", pin.Provider, "fresh_model", fresh.Model, "fresh_provider", fresh.Provider, "prior_tier", catalog.TierFor(pin.Model).String(), "fresh_tier", catalog.TierFor(fresh.Model).String())
		return *res, true
	}
	return TurnRoute{}, false
}

func (t SessionTurn) plan(ctx context.Context, res *TurnRoute) TurnRoute {
	pin, fresh, req := t.Pin, t.Fresh, t.Request
	prefixTokens, prefixKnown := CacheablePrefixTokens(pin, t.PlannerTokens, t.PrefixBroken)
	decision := continuity.Plan(req.Session, fresh, continuity.PlanInputs{
		EstimatedInputTokens:  t.PlannerTokens,
		CacheablePrefixTokens: prefixTokens,
		CachePrefixKnown:      prefixKnown,
		PinCacheCold:          PinCacheCold(pin, t.PrefixBroken, t.Now()),
		AvailableModels:       t.Settings.AvailableModels,
	}, t.Settings.Planner)
	res.PlannerDecision = decision

	outcome := continuity.Settle(decision, t.PinFound, t.PrefixBroken)
	if authoritative, ok := t.freshPickSwitchOutcome(ctx, *res); ok {
		outcome = authoritative
	}
	if outcome.Stay {
		anchor := pin.Decision()
		res.Decision = t.Serving.BandSwap(ctx, res.TurnType, pin, fresh, req)
		res.StickyHit = true
		res.PinTier = outcome.PinTier
		t.Pins.Refresh(ctx, t.InstallationID, res.SessionKey, pin, res.PinRole, anchor)
		return *res
	}

	if outcome.Handover == continuity.HandoverSkippedTrimmedHistory {
		observability.FromContext(ctx).Info("Handover summarizer skipped: client history trim already bounded this switch turn", "pin_model", pin.Model, "fresh_model", fresh.Model)
	}
	if outcome.Handover == continuity.HandoverSummarize {
		var contextEstimate int
		var rewritten bool
		res.Handover, contextEstimate, rewritten = t.Serving.Handover(ctx, req, pin.Model, fresh.Model)
		if rewritten {
			res.ContextEstimate = contextEstimate
		}
	}

	res.Decision = fresh
	if outcome.PinTier != "" {
		res.PinTier = outcome.PinTier
	}
	t.Pins.WriteNew(ctx, t.InstallationID, res.SessionKey, res.PinRole, fresh)
	return *res
}
