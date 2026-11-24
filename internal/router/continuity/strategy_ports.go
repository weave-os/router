package continuity

import (
	"fmt"
	"strings"
	"time"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/planner"
	"weave-os/router/internal/router/sessionpin"
)

type Ownership interface {
	Owns(strategy router.Strategy) bool
	IsDecision(dec router.Decision) bool
}

type DecisionFacts interface {
	ClassifierTelemetryOf(md *router.RoutingMetadata) ClassifierTelemetry
	PinStateOf(pin sessionpin.Pin) PinState
	ClassifierMargin(dec router.Decision) *float64
	CandidateScore(dec router.Decision, servedIdentity, provider, rosterArmID string) *float64
}

type UpgradeJudge interface {
	EvaluateAuthoritativeUpgrade(evidence UpgradeEvidence) UpgradeVerdict
	JudgeAuthoritative(guards AuthoritativeGuards, pin sessionpin.Pin, pinFound bool, fresh router.Decision, inputTokens int) AuthoritativeVerdict
	SlidingThresholdServes(req router.Request, pin sessionpin.Pin, fresh router.Decision) SlidingThreshold
	EffortHysteresisHold(fresh router.Decision, prevServedModel, chosenModel, chosenEffort string) string
	AppendEffortHysteresisReason(reason string) string
	FreshIsMoreExpensive(stayModel, freshModel string, inputTokens int) bool
}

type CostGate interface {
	Decide(gate CostGateConfig, input CostGateInput) (sessionpin.Pin, planner.Decision, bool)
	StayPin(gate CostGateConfig, req router.Request, activePin, history sessionpin.Pin, now time.Time) (sessionpin.Pin, bool)
	Serve(gate CostGateConfig, input CostGateInput, priorServedModel string) CostGateServe
	HistoryReason() string
}

type CostGateServe struct {
	Decision             router.Decision
	Planner              planner.Decision
	Sticky               bool
	StayModel            string
	ChallengerEffort     string
	HeldEffort           string
	PinTier              string
	UpgradeConfidence    float64
	UpgradeConfidenceLow bool
}

type StrategyPorts struct {
	Ownership Ownership
	Facts     DecisionFacts
	Upgrade   UpgradeJudge
	CostGate  CostGate
	History   SwitchHistory
}

func (p StrategyPorts) Validate() error {
	var missing []string
	if p.Ownership == nil {
		missing = append(missing, "Ownership")
	}
	if p.Facts == nil {
		missing = append(missing, "Facts")
	}
	if p.Upgrade == nil {
		missing = append(missing, "Upgrade")
	}
	if p.CostGate == nil {
		missing = append(missing, "CostGate")
	}
	if p.History == nil {
		missing = append(missing, "History")
	}
	if len(missing) > 0 {
		return fmt.Errorf("strategy ports not wired: %s", strings.Join(missing, ", "))
	}
	return nil
}
