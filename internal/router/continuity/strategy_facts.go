package continuity

import (
	"fmt"
	"math"
	"time"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/planner"
	"weave-os/router/internal/router/sessionpin"
)

type UpgradeOutcome string

const (
	UpgradeAllow          UpgradeOutcome = "allow"
	UpgradeHold           UpgradeOutcome = "hold"
	UpgradeExistingPolicy UpgradeOutcome = "existing_policy"
	UpgradeNotApplicable  UpgradeOutcome = "not_applicable"
	UpgradeExcluded       UpgradeOutcome = "excluded"
)

type UpgradeReason string

const (
	UpgradeColdPin              UpgradeReason = "cold_pin"
	UpgradePrefixBroken         UpgradeReason = "prefix_broken"
	UpgradeExplicitQualityFloor UpgradeReason = "explicit_quality_floor"
	UpgradeGroupEscalation      UpgradeReason = "group_escalation"
	UpgradeMarginQualified      UpgradeReason = "margin_qualified"
	UpgradeRepeatedVote         UpgradeReason = "repeated_vote"
	UpgradeWarmPinHold          UpgradeReason = "warm_pin_hold"
	UpgradeInsufficientMargin   UpgradeReason = "insufficient_margin"
	UpgradeMissingGroup         UpgradeReason = "missing_group"
	UpgradeMissingMargin        UpgradeReason = "missing_margin"
	UpgradeInvalidMargin        UpgradeReason = "invalid_margin"
	UpgradeUncalibratedMargin   UpgradeReason = "uncalibrated_margin"
	UpgradeDemotedModel         UpgradeReason = "demoted_model"
	UpgradeIneligibleModel      UpgradeReason = "ineligible_model"
	UpgradeNoPin                UpgradeReason = "no_pin"
	UpgradeSameModel            UpgradeReason = "same_model"
	UpgradeNotMoreExpensive     UpgradeReason = "not_more_expensive"
	UpgradeMissingPricing       UpgradeReason = "missing_pricing"
	UpgradeStalePin             UpgradeReason = "stale_pin"
)

type UpgradeVerdict struct {
	Outcome UpgradeOutcome
	Reason  UpgradeReason
}

func (v UpgradeVerdict) DemotedModel() bool { return v.Reason == UpgradeDemotedModel }

type UpgradeEvidence struct {
	HasPin          bool
	SameModel       bool
	MoreExpensive   bool
	PricingKnown    bool
	FreshEligible   bool
	Demoted         bool
	FreshGroup      escalation.Group
	PinGroup        escalation.Group
	RequestedGroup  escalation.Group
	CacheCold       bool
	PrefixBroken    bool
	Margin          *float64
	MarginQualified *bool
	StalePin        bool
	VoteCount       *int
	RequiredVotes   int
}

type AuthoritativeHold int

const (
	AuthoritativeServe AuthoritativeHold = iota
	AuthoritativeHoldUpgradeConfidenceLow
	AuthoritativeHoldDowngradeConfidenceLow
	AuthoritativeHoldDowngradeHysteresis
)

type AuthoritativeGuards struct {
	ConfidenceThreshold   float64
	UpgradeGate           bool
	DowngradeGate         bool
	HysteresisTurns       int
	ShadowHysteresisTurns int
}

type AuthoritativeVerdict struct {
	Hold                  AuthoritativeHold
	Threshold             float64
	Confidence            float64
	Downgrade             bool
	DowngradeVotes        int
	HysteresisTurns       int
	ShadowHysteresisTurns int
	HysteresisWouldHold   bool
	ConfidenceWouldHold   bool
}

type CostGateConfig struct {
	AvailableModels            map[string]struct{}
	RegisteredProviders        func() map[string]struct{}
	UpgradeConfidenceThreshold float64
}

type CostGateInput struct {
	Request              router.Request
	ActivePin            sessionpin.Pin
	HMMHistory           sessionpin.Pin
	Fresh                router.Decision
	EstimatedInputTokens int
	PrefixBroken         bool
	Planner              planner.EVConfig
	Now                  time.Time
}

const DefaultUpgradeConfidenceThreshold = 0.50

const DefaultDowngradeHysteresisTurns = 2

type AuthoritativeConfig struct {
	UpgradeConfidenceThreshold     float64
	UpgradeGate                    bool
	UpgradePolicy                  flags.AuthoritativeUpgradePolicy
	UpgradeVotes                   int
	UpgradeSlidingThreshold        bool
	UpgradeShadow                  UpgradeShadowConfig
	DowngradeGate                  bool
	DowngradeHysteresisTurns       int
	DowngradeHysteresisShadowTurns int
	AuthorityCacheShadow           bool
}

type UpgradeShadowConfig struct {
	MarginThreshold *float64
	StalePinAfter   time.Duration
}

func DefaultAuthoritativeConfig() AuthoritativeConfig {
	return AuthoritativeConfig{
		UpgradeConfidenceThreshold: DefaultUpgradeConfidenceThreshold,
		UpgradeGate:                true,
		UpgradePolicy:              flags.AuthoritativeUpgradePolicyScore,
		UpgradeVotes:               3,
		DowngradeHysteresisTurns:   DefaultDowngradeHysteresisTurns,
		AuthorityCacheShadow:       true,
	}
}

func (c UpgradeShadowConfig) Validate() error {
	if c.MarginThreshold != nil && (math.IsNaN(*c.MarginThreshold) || math.IsInf(*c.MarginThreshold, 0) || *c.MarginThreshold < 0 || *c.MarginThreshold > 1) {
		return fmt.Errorf("upgrade evidence margin threshold must be finite and within [0, 1]")
	}
	if c.StalePinAfter < 0 {
		return fmt.Errorf("upgrade evidence stale-pin age must be nonnegative")
	}
	return nil
}

type PinState struct {
	PolicyGroup  string
	UpgradeVotes int
}

type ClassifierTelemetry struct {
	Margin         *float64
	PolicyGroup    string
	ArtifactID     string
	ArtifactSHA256 string
	PredictedLabel string
	ClassOrder     []string
	Probabilities  map[string]float64
	SelectionTrace *router.SelectionTrace
}

type SlidingThreshold struct {
	Served          bool
	Probability     float64
	FreshGroup      string
	ClassifiedGroup string
}
