package hmm

import (
	"context"
	"time"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/continuity"
	"weave-os/router/internal/router/planner"
	"weave-os/router/internal/router/sessionpin"
)

type servingPorts struct{}

func ServingPorts() continuity.StrategyPorts {
	ports := servingPorts{}
	return continuity.StrategyPorts{Ownership: ports, Facts: ports, Upgrade: ports, CostGate: ports, History: ports}
}

func (servingPorts) Owns(strategy router.Strategy) bool { return Owns(strategy) }

func (servingPorts) IsDecision(dec router.Decision) bool { return IsDecision(dec) }

func (servingPorts) ClassifierTelemetryOf(md *router.RoutingMetadata) ClassifierTelemetry {
	return ClassifierTelemetryOf(md)
}

func (servingPorts) PinStateOf(pin sessionpin.Pin) PinState { return PinStateOf(pin) }

func (servingPorts) ClassifierMargin(dec router.Decision) *float64 { return ClassifierMargin(dec) }

func (servingPorts) CandidateScore(dec router.Decision, servedIdentity, provider, rosterArmID string) *float64 {
	return CandidateScore(dec, servedIdentity, provider, rosterArmID)
}

func (servingPorts) EvaluateAuthoritativeUpgrade(evidence UpgradeEvidence) UpgradeVerdict {
	return EvaluateAuthoritativeUpgrade(evidence)
}

func (servingPorts) JudgeAuthoritative(guards continuity.AuthoritativeGuards, pin sessionpin.Pin, pinFound bool, fresh router.Decision, inputTokens int) AuthoritativeVerdict {
	return AuthoritativeGuards(guards).Judge(pin, pinFound, fresh, inputTokens)
}

func (servingPorts) SlidingThresholdServes(req router.Request, pin sessionpin.Pin, fresh router.Decision) SlidingThreshold {
	return SlidingThresholdServes(req, pin, fresh)
}

func (servingPorts) EffortHysteresisHold(fresh router.Decision, prevServedModel, chosenModel, chosenEffort string) string {
	return EffortHysteresisHold(fresh, prevServedModel, chosenModel, chosenEffort)
}

func (servingPorts) AppendEffortHysteresisReason(reason string) string {
	return AppendEffortHysteresisReason(reason)
}

func (servingPorts) FreshIsMoreExpensive(stayModel, freshModel string, inputTokens int) bool {
	return FreshIsMoreExpensive(stayModel, freshModel, inputTokens)
}

func (servingPorts) Decide(gate continuity.CostGateConfig, input CostGateInput) (sessionpin.Pin, planner.Decision, bool) {
	return CostGate(gate).Decide(input)
}

func (servingPorts) StayPin(gate continuity.CostGateConfig, req router.Request, activePin, history sessionpin.Pin, now time.Time) (sessionpin.Pin, bool) {
	return CostGate(gate).StayPin(req, activePin, history, now)
}

func (servingPorts) HistoryReason() string { return HistoryReason }

func (servingPorts) HistoryRole(pinRole string) string { return HistoryRole(pinRole) }

func (servingPorts) LoadHistory(ctx context.Context, rows continuity.HistoryRows, sessionKey [sessionpin.SessionKeyLen]byte, pinRole string) sessionpin.Pin {
	return LoadHistory(ctx, rows, sessionKey, pinRole)
}

func (servingPorts) RecordTurnHistory(ctx context.Context, rows continuity.HistoryRows, rec continuity.TurnHistoryRecord) (string, bool) {
	return RecordTurnHistory(ctx, rows, rec)
}
