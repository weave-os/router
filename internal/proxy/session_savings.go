package proxy

import (
	"strings"
	"time"

	"weave-os/router/internal/router/catalog"
)

// Token-efficiency harnesses the control plane measures; Pi has no harness of
// its own and is measured with OpenCode's cells.
const (
	TokenEfficiencyHarnessClaudeCode = "claude-code"
	TokenEfficiencyHarnessCodex      = "codex"
	TokenEfficiencyHarnessOpenCode   = "opencode"
)

const (
	clientAppPi         = "pi"
	clientAppPiSubagent = "pi-subagent"
	utilityReasonSuffix = "_hard_pin"
	weaveModelAlias     = "weave"
)

// savingsBaseline is a comparison model the session savings figure is
// measured against; it is never a dispatch target.
type savingsBaseline struct {
	model string
	label string
}

var (
	savingsBaselineOpus = savingsBaseline{model: "claude-opus-5-5", label: "Opus 5.5"}
	savingsBaselineSol  = savingsBaseline{model: "gpt-6.1-sol", label: "GPT-6.1 Sol"}
)

// savingsComparison picks the measured cells a client's turns are priced against; Pi has no
// harness of its own and reuses opencode's.
type savingsComparison struct {
	harness  string
	baseline savingsBaseline
}

var savingsComparisonByClientApp = map[string]savingsComparison{
	ClientAppClaudeCode: {harness: TokenEfficiencyHarnessClaudeCode, baseline: savingsBaselineOpus},
	ClientAppCodex:      {harness: TokenEfficiencyHarnessCodex, baseline: savingsBaselineSol},
	ClientAppOpencode:   {harness: TokenEfficiencyHarnessOpenCode, baseline: savingsBaselineOpus},
	clientAppPi:         {harness: TokenEfficiencyHarnessOpenCode, baseline: savingsBaselineOpus},
	clientAppPiSubagent: {harness: TokenEfficiencyHarnessOpenCode, baseline: savingsBaselineOpus},
}

// SavingsBaselineModels lists every comparison model a session can be measured against.
func SavingsBaselineModels() []string {
	return []string{savingsBaselineOpus.model, savingsBaselineSol.model}
}

// SessionSavingsTurn is one committed upstream turn's inputs to normalized savings.
type SessionSavingsTurn struct {
	Timestamp                 time.Time
	ClientApp                 string
	RequestedModel            string
	DecisionModel             string
	DecisionProvider          string
	DecisionReason            string
	InputTokens               int64
	OutputTokens              int64
	CacheCreationTokens       int64
	CacheReadTokens           int64
	BaselineWarmPrefillTokens int64
	ActualCostUSDMicros       int64
	SubscriptionServed        bool
}

// TokenEfficiencyValidity is the half-open window a measurement applies to; a
// nil bound is open.
type TokenEfficiencyValidity struct {
	From *time.Time
	To   *time.Time
}

func (v TokenEfficiencyValidity) contains(at time.Time) bool {
	return (v.From == nil || !v.From.After(at)) && (v.To == nil || at.Before(*v.To))
}

// TokenEfficiencyRatio is a measured work ratio for one (harness, comparison model, served model).
type TokenEfficiencyRatio struct {
	Harness       string
	BaselineModel string
	ServedModel   string
	InputPoint    float64
	InputLower    float64
	OutputPoint   float64
	OutputLower   float64
	Native        bool
	Validity      TokenEfficiencyValidity
}

// TokenEfficiencyQualityFactor is a measured pass-rate comparison for one (harness, comparison model).
type TokenEfficiencyQualityFactor struct {
	Harness               string
	BaselineModel         string
	RouterPassRateLower   float64
	BaselinePassRateUpper float64
	Validity              TokenEfficiencyValidity
}

// TokenEfficiencyEvidence is the applied measurement set normalized savings reads.
type TokenEfficiencyEvidence struct {
	Ratios         []TokenEfficiencyRatio
	QualityFactors []TokenEfficiencyQualityFactor
}

// NormalizedSessionSavings is a session's savings against its client's
// comparison model, repriced with measured token efficiency.
type NormalizedSessionSavings struct {
	BaselineModel    string
	BaselineLabel    string
	SavingsUSDMicros int64
}

// ComputeNormalizedSessionSavings mirrors the dashboard's calculated savings:
// a router-chosen API turn saves the comparison model's cost for the work it
// would have done (its cost on the same tokens scaled by the measured ratios)
// less the turn's billed cost scaled by the pass-rate penalty. Subscription,
// forced, utility and unmeasured turns save nothing. ok is false when the
// session's comparison has no measurements at all.
func ComputeNormalizedSessionSavings(turns []SessionSavingsTurn, evidence TokenEfficiencyEvidence) (savings NormalizedSessionSavings, ok bool) {
	var savingsUSD float64
	for _, turn := range turns {
		comparison, known := savingsComparisonByClientApp[strings.ToLower(turn.ClientApp)]
		if !known || !evidence.measures(comparison) {
			continue
		}
		savings.BaselineModel = comparison.baseline.model
		savings.BaselineLabel = comparison.baseline.label
		ok = true
		savingsUSD += turnSavingsUSD(turn, comparison, evidence)
	}
	savings.SavingsUSDMicros = catalog.SignedUSDToMicros(savingsUSD)
	return savings, ok
}

func (e TokenEfficiencyEvidence) measures(comparison savingsComparison) bool {
	for _, ratio := range e.Ratios {
		if ratio.Harness == comparison.harness && ratio.BaselineModel == comparison.baseline.model {
			return true
		}
	}
	return false
}

func turnSavingsUSD(turn SessionSavingsTurn, comparison savingsComparison, evidence TokenEfficiencyEvidence) float64 {
	if turn.SubscriptionServed || !scorerSubstitution(turn) {
		return 0
	}
	ratio, measured := evidence.ratioAt(comparison, turn.DecisionModel, turn.Timestamp)
	if !measured {
		return 0
	}
	penalty, measured := evidence.passRatePenaltyAt(comparison, turn.Timestamp)
	if !measured {
		return 0
	}
	price, priced := catalog.PrimaryPriceFor(comparison.baseline.model)
	if !priced {
		return 0
	}
	in, out := int(turn.InputTokens), int(turn.OutputTokens)
	cacheCreation, cacheRead := int(turn.CacheCreationTokens), int(turn.CacheReadTokens)
	warmPrefill := min(int(turn.BaselineWarmPrefillTokens), cacheCreation)
	baselineInputCost := catalog.CounterfactualInputCost(in, cacheCreation, cacheRead, warmPrefill, price, turn.DecisionProvider, catalog.UsageModifiers{})
	baselineOutputCost := catalog.EffectiveOutputCost(catalog.PromptTokens(in, cacheCreation, cacheRead, turn.DecisionProvider), out, price, catalog.UsageModifiers{})
	actual := float64(turn.ActualCostUSDMicros) / 1_000_000
	return baselineInputCost*cappedBound(ratio.InputPoint, ratio.InputLower, ratio.Native) +
		baselineOutputCost*cappedBound(ratio.OutputPoint, ratio.OutputLower, ratio.Native) -
		actual*penalty
}

// scorerSubstitution reports whether the router's scorer chose the turn's model:
// forced and utility hard-pinned turns keep the model the client asked for.
func scorerSubstitution(turn SessionSavingsTurn) bool {
	reason := strings.TrimPrefix(turn.DecisionReason, AllowlistOverrideReasonPrefix)
	return !strings.HasPrefix(reason, loopActionUserForced) &&
		!strings.HasSuffix(reason, utilityReasonSuffix) &&
		strings.ToLower(turn.RequestedModel) != weaveModelAlias
}

func (e TokenEfficiencyEvidence) ratioAt(comparison savingsComparison, servedModel string, at time.Time) (TokenEfficiencyRatio, bool) {
	for _, ratio := range e.Ratios {
		if ratio.Harness == comparison.harness && ratio.BaselineModel == comparison.baseline.model &&
			ratio.ServedModel == servedModel && ratio.Validity.contains(at) {
			return ratio, true
		}
	}
	return TokenEfficiencyRatio{}, false
}

// passRatePenaltyAt leaves OpenCode unadjusted, and any harness without a
// pass-rate measurement in force. measured is false only when the router's
// lower pass rate is zero, where no finite penalty exists.
func (e TokenEfficiencyEvidence) passRatePenaltyAt(comparison savingsComparison, at time.Time) (penalty float64, measured bool) {
	if comparison.harness == TokenEfficiencyHarnessOpenCode {
		return 1, true
	}
	for _, factor := range e.QualityFactors {
		if factor.Harness != comparison.harness || factor.BaselineModel != comparison.baseline.model || !factor.Validity.contains(at) {
			continue
		}
		if factor.RouterPassRateLower <= 0 {
			return 0, false
		}
		return max(factor.BaselinePassRateUpper/factor.RouterPassRateLower, 1), true
	}
	return 1, true
}

// cappedBound takes the point estimate for a model in its own harness and the
// lower bound otherwise, never above 1, so applying it can only reduce savings.
func cappedBound(point, lower float64, native bool) float64 {
	if native {
		return min(point, 1)
	}
	return min(lower, 1)
}
