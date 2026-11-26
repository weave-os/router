package routingpolicy

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/candidates"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/continuity"
	"weave-os/router/internal/router/escalation/floor"
	"weave-os/router/internal/router/handover"
	"weave-os/router/internal/router/planner"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/turntype"
)

type AuthoritativePinTier string

const (
	PinTierAuthoritativeSlidingThresholdUpgrade AuthoritativePinTier = "authoritative_sliding_threshold_upgrade"
	PinTierAuthoritativeUpgradeConfidenceLow    AuthoritativePinTier = "authoritative_hmm_upgrade_confidence_low"
	PinTierAuthoritativeDowngradeConfidenceLow  AuthoritativePinTier = "authoritative_hmm_downgrade_confidence_low"
	PinTierAuthoritativeDowngradeHysteresis     AuthoritativePinTier = "authoritative_hmm_downgrade_hysteresis"
	PinTierAuthoritativeExcludedPin             AuthoritativePinTier = "authoritative_excluded_pin"
	PinTierAuthoritativeExcludedReroute         AuthoritativePinTier = "authoritative_excluded_reroute"
)

type SessionSettings struct {
	UpgradePolicy                  flags.AuthoritativeUpgradePolicy
	UpgradeGate                    bool
	UpgradeVotes                   int
	DowngradeGate                  bool
	DowngradeHysteresisTurns       int
	DowngradeHysteresisShadowTurns int
	CacheShadowEnabled             bool
	HMMConfig                      continuity.AuthoritativeConfig
	AvailableModels                map[string]struct{}
	CostGate                       continuity.CostGateConfig
	Planner                        planner.EVConfig
	PlannerEnabled                 bool
	ContinuityPlanner              planner.EVConfig
	ScoreToolResultTurns           bool
	HMMToolResultHold              bool
}

type SessionTurn struct {
	Strategy        router.Strategy
	Request         router.Request
	Pin             sessionpin.Pin
	PinFound        bool
	SwitchHistory   sessionpin.Pin
	Fresh           router.Decision
	PrefixBroken    bool
	PlannerTokens   int
	Pins            SessionPins
	InstallationID  uuid.UUID
	Ports           continuity.StrategyPorts
	Scorer          router.Router
	Settings        SessionSettings
	Serving         SessionServing
	Now             func() time.Time
	Escalation      *floor.Turn
	ForcedTierFloor catalog.Tier
}

type SessionServing interface {
	BandSwap(ctx context.Context, turnType turntype.TurnType, pin sessionpin.Pin, fresh router.Decision, req router.Request) router.Decision
	Handover(ctx context.Context, req router.Request, pinModel, freshModel string) (outcome handover.Outcome, contextEstimate int, rewritten bool)
	TryServeScorerFallback(ctx context.Context, res *TurnRoute, err error, req router.Request) bool
}

func (t SessionTurn) Serve(ctx context.Context, res *TurnRoute) (TurnRoute, error) {
	log := observability.FromContext(ctx)
	activePin := sessionpin.Pin{}
	if t.PinFound {
		activePin = t.Pin
	}
	res.UpgradeShadow = t.UpgradeDecision(activePin, *res)
	res.AuthorityShadow = t.cacheShadow(activePin)
	logAuthorityCacheShadow(ctx, *res)
	if res.UpgradeShadow != nil && res.UpgradeShadow.Verdict.DemotedModel() {
		return t.serveExcluded(ctx, res)
	}
	sliding := t.Ports.Upgrade.SlidingThresholdServes(t.Request, t.Pin, t.Fresh)
	if sliding.Served {
		log.Info("authoritative sliding threshold serving fresh model",
			"pin_model", t.Pin.Model,
			"pin_group", t.Request.Session.SlidingThresholdPinGroup,
			"fresh_model", t.Fresh.Model,
			"fresh_group", sliding.FreshGroup,
			"classified_group", sliding.ClassifiedGroup,
			"probability", sliding.Probability,
		)
	}
	verdict := t.Ports.Upgrade.JudgeAuthoritative(t.guards(sliding.Served), t.Pin, t.PinFound, t.Fresh, t.PlannerTokens)
	if t.holdPin(ctx, res, verdict) {
		return *res, nil
	}
	if t.holdPerTurnPickForSwitchVerdict(ctx, res) {
		t.Pins.Refresh(ctx, t.InstallationID, res.SessionKey, t.Pin, res.PinRole, res.Decision)
		return *res, nil
	}
	res.Decision = t.Fresh
	res.PinTier = "authoritative_per_turn"
	if sliding.Served {
		res.PinTier = string(PinTierAuthoritativeSlidingThresholdUpgrade)
	}
	t.Pins.WriteNew(ctx, t.InstallationID, res.SessionKey, res.PinRole, t.Fresh)
	return *res, nil
}

func (t SessionTurn) serveExcluded(ctx context.Context, res *TurnRoute) (TurnRoute, error) {
	log := observability.FromContext(ctx)
	if t.PinFound && candidates.AutomaticPinEligible(t.Pin, t.Request) {
		decision := t.Pin.Decision()
		res.Decision = decision
		res.StickyHit = true
		res.PinTier = string(PinTierAuthoritativeExcludedPin)
		log.Warn("authoritative policy returned an excluded model; keeping session pin",
			"pin_model", t.Pin.Model,
			"fresh_model", t.Fresh.Model,
			"reason", res.UpgradeShadow.Verdict.Reason,
		)
		t.Pins.Refresh(ctx, t.InstallationID, res.SessionKey, t.Pin, res.PinRole, decision)
		return *res, nil
	}

	rerouted, rerouteErr := t.Scorer.Route(ctx, t.Request)
	if rerouteErr != nil {
		return *res, rerouteErr
	}
	if !ExcludedRerouteServable(rerouted, t.Request, t.Settings.AvailableModels) {
		return *res, fmt.Errorf("authoritative policy returned excluded model %q after reroute: %w", rerouted.Model, router.ErrNoEligibleProvider)
	}
	res.Fresh = rerouted
	res.Decision = rerouted
	res.PinTier = string(PinTierAuthoritativeExcludedReroute)
	log.Warn("authoritative policy returned an excluded model; serving rerouted decision",
		"excluded_model", t.Fresh.Model,
		"rerouted_model", rerouted.Model,
		"reason", res.UpgradeShadow.Verdict.Reason,
	)
	t.Pins.WriteNew(ctx, t.InstallationID, res.SessionKey, res.PinRole, rerouted)
	return *res, nil
}

func (t SessionTurn) guards(slidingServed bool) continuity.AuthoritativeGuards {
	return continuity.AuthoritativeGuards{
		ConfidenceThreshold:   t.Settings.HMMConfig.UpgradeConfidenceThreshold,
		UpgradeGate:           t.Settings.UpgradeGate && t.Settings.UpgradePolicy != flags.AuthoritativeUpgradePolicyOff && !slidingServed,
		DowngradeGate:         t.Settings.DowngradeGate,
		HysteresisTurns:       t.Settings.DowngradeHysteresisTurns,
		ShadowHysteresisTurns: t.Settings.DowngradeHysteresisShadowTurns,
	}
}

func (t SessionTurn) holdPin(ctx context.Context, res *TurnRoute, verdict continuity.AuthoritativeVerdict) bool {
	if !t.Ports.Ownership.Owns(res.Strategy) {
		return false
	}
	log := observability.FromContext(ctx)
	pin, fresh := t.Pin, t.Fresh
	fields := []any{
		"pin_model", pin.Model,
		"pin_provider", pin.Provider,
		"fresh_model", fresh.Model,
		"fresh_provider", fresh.Provider,
		"confidence", verdict.Confidence,
		"threshold", verdict.Threshold,
	}
	if verdict.Downgrade {
		fields = append(fields, "downgrade_votes", verdict.DowngradeVotes, "hysteresis_turns", verdict.HysteresisTurns)
	}
	if verdict.Hold == continuity.AuthoritativeServe {
		if verdict.Downgrade {
			res.DowngradeShadow = DowngradeGuardShadow{
				Served:              true,
				Votes:               verdict.DowngradeVotes,
				HysteresisWouldHold: verdict.HysteresisWouldHold,
				ConfidenceWouldHold: verdict.ConfidenceWouldHold,
			}
			fields = append(fields, "shadow_hysteresis_turns", verdict.ShadowHysteresisTurns, "shadow_hysteresis_would_hold", verdict.HysteresisWouldHold, "shadow_confidence_would_hold", verdict.ConfidenceWouldHold)
			log.Info("turnloop served authoritative downgrade", fields...)
		}
		return false
	}
	decision := pin.Decision()
	res.Decision, res.UserForced = decision, false
	res.StickyHit = true
	switch verdict.Hold {
	case continuity.AuthoritativeHoldUpgradeConfidenceLow:
		res.PinTier = string(PinTierAuthoritativeUpgradeConfidenceLow)
		log.Info("turnloop suppressed low-confidence authoritative upgrade; keeping session pin", fields...)
		t.Pins.Refresh(ctx, res.InstallationID, res.SessionKey, pin, res.PinRole, decision)
	case continuity.AuthoritativeHoldDowngradeConfidenceLow:
		res.PinTier = string(PinTierAuthoritativeDowngradeConfidenceLow)
		log.Info("turnloop suppressed low-confidence authoritative downgrade; keeping session pin", fields...)
		t.Pins.RefreshDowngradeVotes(ctx, res.InstallationID, res.SessionKey, pin, res.PinRole, decision, verdict.DowngradeVotes)
	default:
		res.PinTier = string(PinTierAuthoritativeDowngradeHysteresis)
		log.Info("turnloop held authoritative downgrade below the hysteresis threshold; keeping session pin", fields...)
		t.Pins.RefreshDowngradeVotes(ctx, res.InstallationID, res.SessionKey, pin, res.PinRole, decision, verdict.DowngradeVotes)
	}
	return true
}
