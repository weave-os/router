package routingpolicy

import (
	"context"

	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/escalation/floor"
)

func (t SessionTurn) RouteEscalated(ctx context.Context, helper *floor.Helper, facts floor.Facts, res TurnRoute) (TurnRoute, error) {
	baselineTurn, baselineRes := t, res
	escalationTurn := helper.Begin(ctx, facts, t.Request)
	escalationTurn.Apply(&t.Request)
	res.SetEscalation(escalationTurn)
	if t.PinFound {
		t.Request.Session.PreviousPolicyGroup = escalation.Group(t.Ports.Facts.PinStateOf(t.Pin).PolicyGroup)
	}
	t.Escalation, baselineTurn.Escalation = escalationTurn, escalationTurn

	res, routeErr := t.Route(ctx, &res)
	if escalationTurn != nil {
		committed, rerunBaseline, err := helper.Settle(ctx, escalationTurn, res.Decision, DecisionPolicyGroup(res.Decision), routeErr)
		if rerunBaseline {
			res = baselineRes
			res, routeErr = baselineTurn.Route(ctx, &res)
		} else if err == nil {
			res.EscalationShadowMarked, res.EscalationScope, res.EscalationOrdinal = committed.ShadowMarked, committed.Scope, committed.Ordinal
		}
	}
	res.SetEscalation(escalationTurn)
	if helper.SettleLLM(ctx, escalationTurn, res.Decision, routeErr) {
		res = baselineRes
		res, routeErr = baselineTurn.Route(ctx, &res)
		res.SetEscalation(escalationTurn)
	}
	return res, routeErr
}
