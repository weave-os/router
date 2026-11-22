package routingpolicy

import (
	"time"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/breaker"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/continuity"
	"weave-os/router/internal/router/escalation/floor"
	"weave-os/router/internal/router/handover"
	"weave-os/router/internal/router/planner"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
)

type TurnRoute struct {
	EscalationShadowMarked bool
	EscalationScope        [32]byte
	EscalationOrdinal      int64
	escalation             *floor.Turn

	Decision                    router.Decision
	SessionKey                  [sessionpin.SessionKeyLen]byte
	InstallationID              uuid.UUID
	Strategy                    router.Strategy
	TurnType                    turntype.TurnType
	StickyHit                   bool
	HardPinned                  bool
	UserForced                  bool
	SessionFirstTurn            bool
	Purpose                     inference.Purpose
	Origin                      policy.OverrideSource
	AuthoritativePerTurn        bool
	UsageBypass                 bool
	CallerModelPassthrough      bool
	PinTier                     string
	PinAgeSec                   int64
	ForcedPinDropped            bool
	ForcedPinDropReason         string
	ForcedPinModel              string
	PinProvider                 string
	PrefixBroken                bool
	PriorTurnGapMS              *int64
	PolicyFallback              bool
	RequestedTier               catalog.Tier
	PinRole                     string
	StickyRole                  string
	Fresh                       router.Decision
	PlannerDecision             planner.Decision
	PinModel                    string
	PinPolicyGroup              string
	PriorServedModel            string
	SessionEverSwitched         bool
	PriorServedEndedAt          time.Time
	PriorPromptTokens           int
	TokenizerCalibration        sessionpin.TokenizerCalibration
	ContextEstimate             int
	StripThinkingBlocks         bool
	Handover                    handover.Outcome
	SuggestionMode              bool
	PrefixTrimmed               bool
	EscalateEffort              bool
	PinTurnCount                int
	PinFirstPinnedAt            time.Time
	SessionDisabledProviders    []string
	TranslationEnabledProviders map[string]struct{}
	SessionDemotedModels        []string
	ObservedOverflowModels      []string
	SessionCooldownModels       map[string]time.Time
	CooldownProbeReleases       []func()
	OverloadProbe               *breaker.Probe
	CooldownProbes              map[string]time.Time
	CooldownProbeDeniedModels   []string
	SessionStrikeReadmitModels  []string
	AuthorityShadow             AuthorityCacheShadow
	UpgradeShadow               *AuthoritativeUpgradeDecision
	DowngradeShadow             DowngradeGuardShadow
}

func (r TurnRoute) ReleaseCooldownProbes() {
	for _, release := range r.CooldownProbeReleases {
		release()
	}
}

type DowngradeGuardShadow struct {
	Served              bool
	Votes               int
	HysteresisWouldHold bool
	ConfidenceWouldHold bool
}

type AuthorityCacheShadow struct {
	Computed     bool
	Decision     planner.Decision
	StayModel    string
	StayProvider string
	Sticky       bool
	StayScore    *float64
	FreshScore   *float64
}

func (a AuthorityCacheShadow) Reason() string {
	if !a.Computed {
		return ""
	}
	return a.Decision.Reason
}

func (a AuthorityCacheShadow) EVRan() bool {
	return a.Computed && a.Decision.ShadowComputed
}

func (r TurnRoute) ModelSwitched() bool {
	transition := r.PriorServedModel != "" && r.PriorServedModel != r.Decision.ServedIdentity()
	return transition || r.SessionEverSwitched || r.StripThinkingBlocks
}

func (r TurnRoute) DispatchPurpose(surface inference.Purpose) inference.Purpose {
	if len(r.Purpose) > 0 {
		return r.Purpose
	}
	return surface
}

func (r TurnRoute) RescueOrigin() policy.OverrideSource {
	if len(r.Purpose) > 0 {
		return policy.OverrideSourceDeployment
	}
	return ""
}

type AuthoritativeUpgradeDecision struct {
	Mode             flags.AuthoritativeUpgradePolicy
	Evidence         continuity.UpgradeEvidence
	Verdict          continuity.UpgradeVerdict
	PinModel         string
	PinProvider      string
	PinTier          catalog.Tier
	RequestedTier    catalog.Tier
	PinAgeSec        *int64
	MarginThreshold  *float64
	StalePinAfterSec *int64
	FreshScore       *float64
	Applied          bool
}

func (r *TurnRoute) ServeForceModelPin(forceModelPin sessionpin.Pin) {
	r.Decision = forceModelPin.Decision()
	r.Decision.Reason = translate.ReasonUserForceModel
	r.UserForced = true
	r.StickyHit = true
	r.PinTier = translate.ReasonUserForceModel
}

func (r TurnRoute) Escalation() *floor.Turn { return r.escalation }

func (r *TurnRoute) SetEscalation(turn *floor.Turn) { r.escalation = turn }
