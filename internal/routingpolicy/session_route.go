package routingpolicy

import (
	"context"
	"strings"
	"time"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/candidates"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/continuity"
	"weave-os/router/internal/router/escalation/floor"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/turntype"
)

const UnscorableHMMStickyReason = "hmm_no_user_boundary_sticky"

func (t SessionTurn) Route(ctx context.Context, res *TurnRoute) (TurnRoute, error) {
	pin := t.Pin
	t.Request.Session.Pin = router.SessionPin{Found: t.PinFound, Provider: pin.Provider, Model: pin.Model, LastTurnEndedAt: pin.LastTurnEndedAt, LastOutputTokens: pin.LastOutputTokens}
	req := t.Request
	holdToolResult := res.AuthoritativePerTurn && req.Escalation == nil &&
		res.TurnType == turntype.ToolResult && t.Settings.HMMToolResultHold
	if holdToolResult {
		res.AuthoritativePerTurn = false
	}
	if served, ok := t.unscorableSticky(ctx, res); ok {
		return served, nil
	}
	turn := continuity.Turn{IsToolResult: res.TurnType == turntype.ToolResult, Escalated: req.Escalation != nil, AuthoritativePerTurn: res.AuthoritativePerTurn}
	continuityCfg := continuity.Config{PlannerEnabled: t.Settings.PlannerEnabled, ScoreToolResultTurns: t.Settings.ScoreToolResultTurns, HoldToolResult: holdToolResult, Planner: t.Settings.ContinuityPlanner}
	if continuity.ToolResultSticky(req.Session, turn, continuityCfg) {
		return t.reusePin(ctx, res, "postgres_tool_result_sc"), nil
	}
	if continuity.CanReusePin(req.Session, turn, continuityCfg) {
		return t.reusePin(ctx, res, "postgres"), nil
	}

	fresh, fellBack, err := t.score(ctx, res)
	if err != nil {
		return *res, err
	}
	if fellBack {
		return *res, nil
	}
	observability.FromContext(ctx).Info("turnloop scorer decision",
		"fresh_model", fresh.Model,
		"fresh_provider", fresh.Provider,
		"fresh_reason", fresh.Reason,
	)
	res.Fresh = fresh
	t.Fresh = fresh
	if floor.RoutingApplied(fresh) {
		res.Decision = fresh
		res.PinTier = t.Escalation.PinTier()
		return *res, nil
	}
	if res.AuthoritativePerTurn {
		return t.Serve(ctx, res)
	}
	if t.Ports.Ownership.IsDecision(fresh) {
		return t.serveCostGate(ctx, res), nil
	}
	return t.SettleFresh(ctx, res)
}

func (t SessionTurn) unscorableSticky(ctx context.Context, res *TurnRoute) (TurnRoute, bool) {
	pin, req := t.Pin, t.Request
	if req.Escalation != nil || !t.PinFound || !candidates.AutomaticPinEligible(pin, req) || req.ConversationMessages == nil ||
		!t.Ports.Ownership.Owns(t.Strategy) ||
		HasTextUserBoundary(req.ConversationMessages) {
		return TurnRoute{}, false
	}
	if _, honoured := router.HonouredPolicyPin(ctx); honoured {
		return TurnRoute{}, false
	}
	decision := pin.Decision()
	decision.Reason = UnscorableHMMStickyReason
	res.Decision = decision
	res.StickyHit = true
	res.PinTier = UnscorableHMMStickyReason
	observability.FromContext(ctx).Info("HMM turn has no user text; preserving eligible session pin",
		"pin_model", pin.Model, "pin_provider", pin.Provider)
	t.Pins.Refresh(ctx, t.InstallationID, res.SessionKey, pin, res.PinRole, pin.Decision())
	return *res, true
}

func (t SessionTurn) reusePin(ctx context.Context, res *TurnRoute, pinTier string) TurnRoute {
	decision := t.Pin.Decision()
	res.Decision = decision
	res.StickyHit = true
	res.PinTier = pinTier
	t.Pins.Refresh(ctx, t.InstallationID, res.SessionKey, t.Pin, res.PinRole, decision)
	return *res
}

func (t SessionTurn) score(ctx context.Context, res *TurnRoute) (fresh router.Decision, fellBack bool, err error) {
	log := observability.FromContext(ctx)
	req := t.Request
	if t.ForcedTierFloor != catalog.TierUnknown {
		if constrained, ok := candidates.RestrictToTier(req.Candidates.ExcludedModels, t.ForcedTierFloor, t.Settings.AvailableModels); ok {
			tierReq := req
			tierReq.Candidates.ExcludedModels = constrained
			if dec, derr := t.Scorer.Route(ctx, tierReq); derr == nil {
				log.Info("user-forced model evicted; rerouted to next-best in same tier",
					"forced_tier", t.ForcedTierFloor.String(),
					"fresh_model", dec.Model,
					"fresh_provider", dec.Provider,
				)
				return dec, false, nil
			} else if res.AuthoritativePerTurn {
				return router.Decision{}, false, derr
			} else {
				log.Info("tier-constrained reroute found no candidate; using unconstrained scorer",
					"forced_tier", t.ForcedTierFloor.String(), "err", derr)
			}
		}
	}
	dec, err := t.Scorer.Route(ctx, req)
	if err != nil {
		if t.Serving.TryServeScorerFallback(ctx, res, err, req) {
			return router.Decision{}, true, nil
		}
		log.Error("turnloop scorer failed", "err", err, "requested_model", req.RequestedModel)
		return router.Decision{}, false, err
	}
	return dec, false, nil
}

func (t SessionTurn) serveCostGate(ctx context.Context, res *TurnRoute) TurnRoute {
	log := observability.FromContext(ctx)
	activePin := sessionpin.Pin{}
	if t.PinFound {
		activePin = t.Pin
	}
	gateInput := continuity.CostGateInput{
		Request: t.Request, ActivePin: activePin, HMMHistory: t.SwitchHistory, Fresh: t.Fresh,
		EstimatedInputTokens: t.PlannerTokens, PrefixBroken: t.PrefixBroken,
	}
	serveInput := gateInput
	serveInput.Planner = t.Settings.Planner
	serveInput.Now = t.Now()
	served := t.Ports.CostGate.Serve(t.Settings.CostGate, serveInput, res.PriorServedModel)
	res.Decision, res.PlannerDecision, res.PinTier = served.Decision, served.Planner, served.PinTier
	if served.HeldEffort != "" {
		log.Info("HMM effort hysteresis held incumbent",
			"incumbent", res.PriorServedModel,
			"challenger_effort", served.ChallengerEffort,
			"serving_effort", served.HeldEffort,
		)
	}
	if served.StayModel != "" {
		res.PinModel = served.StayModel
		if hmmPin, ok := t.Ports.CostGate.StayPin(t.Settings.CostGate, t.Request, activePin, t.SwitchHistory, t.Now()); ok && hmmPin.Model == served.StayModel {
			ApplyPinEvidence(t.Ports, res, hmmPin, t.Now())
		}
	}
	if served.Sticky {
		res.StickyHit = true
		res.StickyRole = t.Ports.History.HistoryRole(res.PinRole)
	}
	shadowStayPin, _ := t.Ports.CostGate.StayPin(t.Settings.CostGate, t.Request, activePin, t.SwitchHistory, t.Now())
	logSwitchShadow(ctx, switchShadowTurn{StayPin: shadowStayPin, Gate: gateInput, Served: served, Now: t.Now()}, *res)
	return *res
}

func ApplyPinEvidence(ports continuity.StrategyPorts, res *TurnRoute, pin sessionpin.Pin, now time.Time) {
	res.PinModel = pin.Model
	res.PinProvider = pin.Provider
	res.PinPolicyGroup = ports.Facts.PinStateOf(pin).PolicyGroup
	res.PinAgeSec = PinAge(pin, now)
	if !pin.LastTurnEndedAt.IsZero() {
		gapMS := now.Sub(pin.LastTurnEndedAt).Milliseconds()
		res.PriorTurnGapMS = &gapMS
	}
}

func HasTextUserBoundary(messages []router.ConversationMessage) bool {
	for _, message := range messages {
		if strings.EqualFold(message.Role, "user") && strings.TrimSpace(message.Text) != "" {
			return true
		}
	}
	return false
}
