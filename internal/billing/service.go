package billing

import (
	"context"

	"workweave/router/internal/observability"
	"workweave/router/internal/router/catalog"
)

// hasOverrideContextKeyT lives in billing (not middleware/proxy) so both
// sides can reference it without a layering violation.
type hasOverrideContextKeyT struct{}

// HasOverrideContextKey flags override-pass-through requests for the
// proxy's debit hook. Set by middleware.WithBalanceCheck. Bool value.
var HasOverrideContextKey = hasOverrideContextKeyT{}

// HasOverrideFromContext returns true when WithBalanceCheck flagged the
// current request as a billing-override pass-through.
func HasOverrideFromContext(ctx context.Context) bool {
	v := ctx.Value(HasOverrideContextKey)
	if v == nil {
		return false
	}
	b, _ := v.(bool)
	return b
}

// subscriptionOnlyContextKeyT lives in billing (not middleware/proxy) so both
// sides can reference it without a layering violation, mirroring
// hasOverrideContextKeyT.
type subscriptionOnlyContextKeyT struct{}

// SubscriptionOnlyContextKey flags a request whose org balance is depleted (or
// missing) while the request presents a covering subscription credential. Set by
// middleware.WithBalanceCheck instead of returning a 402: the proxy must serve
// the turn on the caller's own subscription (no paid failover, no debit) or
// refuse it. Bool value.
var SubscriptionOnlyContextKey = subscriptionOnlyContextKeyT{}

// WithSubscriptionOnly marks ctx so the proxy serves the turn subscription-only
// (see SubscriptionOnlyContextKey).
func WithSubscriptionOnly(ctx context.Context) context.Context {
	return context.WithValue(ctx, SubscriptionOnlyContextKey, true)
}

// SubscriptionOnlyFromContext reports whether WithBalanceCheck flagged the
// current request as subscription-only because prepaid credits are unavailable
// but the request presents a covering subscription credential.
func SubscriptionOnlyFromContext(ctx context.Context) bool {
	v := ctx.Value(SubscriptionOnlyContextKey)
	if v == nil {
		return false
	}
	b, _ := v.(bool)
	return b
}

// EntryTypeInference is the canonical entry_type for per-request debits.
// Keep in sync with the CHECK constraint on
// router.organization_credit_ledger.entry_type.
const EntryTypeInference = "inference"

// EntryTypeByokFee is the entry_type for Weave's platform fee on a turn served
// by the customer's own provider key. Keep in sync with the CHECK constraint on
// router.organization_credit_ledger.entry_type.
const EntryTypeByokFee = "byok_fee"

// DefaultByokFeeRate is the BYOK platform fee fraction; zero means no fee unless BYOK_FEE_RATE opts a deployment in.
const DefaultByokFeeRate = 0.0

// MinBalanceMicros: requests 402 when balance <= this. 0 matches
// OpenAI/Anthropic prepaid semantics — block at zero, let in-flight
// debits settle.
const MinBalanceMicros int64 = 0

// Service orchestrates balance reads and debits. No I/O of its own — all
// persistence flows through the Repo interface.
type Service struct {
	repo        Repo
	autopay     AutopayNotifier
	byokFeeRate float64
}

// NewService constructs a billing service. The Repo is required; nil panics
// at request time, so the composition root must guard against it.
func NewService(repo Repo) *Service {
	return &Service{repo: repo, byokFeeRate: DefaultByokFeeRate}
}

// WithByokFeeRate sets the BYOK platform fee as a fraction of upstream cost (e.g. 0.05 = 5%). Negative rates clamp to zero.
func (s *Service) WithByokFeeRate(rate float64) *Service {
	if rate < 0 {
		rate = 0
	}
	s.byokFeeRate = rate
	return s
}

// AutopayNotifier signals the control plane that an org's balance just
// crossed below its autopay threshold. Implemented by a Pub/Sub adapter in
// internal/pubsub; nil disables the crossing check (selfhosted, or topic
// env unset).
type AutopayNotifier interface {
	NotifyRechargeNeeded(organizationID string)
}

// WithAutopayNotifier attaches the autopay recharge signaller and returns
// the service for chaining. Wired only in managed mode.
func (s *Service) WithAutopayNotifier(n AutopayNotifier) *Service {
	s.autopay = n
	return s
}

// CheckResult is the outcome of a preflight balance check. HasOverride
// tells the middleware to skip the threshold comparison and flag the
// request context so the debit hook writes a delta=0 ledger row.
type CheckResult struct {
	HasOverride   bool
	BalanceMicros int64
}

// APIKeySpendCapResult is the outcome of a preflight per-key spend-cap
// check. Found is false if the key was deleted mid-request; CapMicros is
// nil for an uncapped key. Middleware blocks when Found && CapMicros !=
// nil && SpentMicros >= *CapMicros.
type APIKeySpendCapResult struct {
	Found       bool
	SpentMicros int64
	CapMicros   *int64
}

// CheckAPIKeySpendCap reads fresh from the repo (not the auth cache) so a
// hot cached key can't overrun its cap within the cache TTL.
func (s *Service) CheckAPIKeySpendCap(ctx context.Context, apiKeyID string) (APIKeySpendCapResult, error) {
	spent, cap, found, err := s.repo.GetAPIKeySpend(ctx, apiKeyID)
	if err != nil {
		return APIKeySpendCapResult{}, err
	}
	return APIKeySpendCapResult{Found: found, SpentMicros: spent, CapMicros: cap}, nil
}

// CheckBalance short-circuits the balance read when an override is active
// (returns HasOverride=true). Otherwise the caller compares BalanceMicros
// against MinBalanceMicros.
//
// ErrBalanceRowMissing means no balance row exists for the org — treat as
// 402; the org needs to be backfilled before inference can succeed.
func (s *Service) CheckBalance(ctx context.Context, orgID string) (CheckResult, error) {
	override, err := s.repo.HasActiveOverride(ctx, orgID)
	if err != nil {
		return CheckResult{}, err
	}
	if override {
		return CheckResult{HasOverride: true}, nil
	}
	balance, err := s.repo.GetBalance(ctx, orgID)
	if err != nil {
		return CheckResult{}, err
	}
	return CheckResult{BalanceMicros: balance}, nil
}

// MonthlySpendResult carries a current-UTC-month spend counter alongside the
// limit that applies to it. LimitMicros nil means no limit is configured.
type MonthlySpendResult struct {
	SpentMicros int64
	LimitMicros *int64
}

// LimitReached reports whether a configured limit has been met or exceeded.
func (r MonthlySpendResult) LimitReached() bool {
	return r.LimitMicros != nil && r.SpentMicros >= *r.LimitMicros
}

// CheckUserMonthlySpend reads the engineer's current-month spend and
// effective monthly limit fresh from Postgres.
func (s *Service) CheckUserMonthlySpend(ctx context.Context, organizationID, routerUserID string) (MonthlySpendResult, error) {
	spent, limit, err := s.repo.GetUserMonthlySpendAndLimit(ctx, organizationID, routerUserID)
	if err != nil {
		return MonthlySpendResult{}, err
	}
	return MonthlySpendResult{SpentMicros: spent, LimitMicros: limit}, nil
}

// CheckOrgMonthlySpend reads the org's current-month spend and configured
// monthly cap fresh from Postgres.
func (s *Service) CheckOrgMonthlySpend(ctx context.Context, organizationID string) (MonthlySpendResult, error) {
	spent, limit, err := s.repo.GetOrgMonthlySpendAndLimit(ctx, organizationID)
	if err != nil {
		return MonthlySpendResult{}, err
	}
	return MonthlySpendResult{SpentMicros: spent, LimitMicros: limit}, nil
}

// DebitInferenceParams is the input to DebitForInference. Token counts
// and pricing come from the proxy's usage extractor; HasOverride is
// carried from the context flag middleware already stamped, so the
// override read doesn't happen twice.
type DebitInferenceParams struct {
	OrganizationID  string
	RouterRequestID string
	Model           string
	Provider        string
	InputTokens     int
	OutputTokens    int
	CacheCreation   int
	CacheRead       int
	Pricing         catalog.Pricing
	HasOverride     bool
	// SubscriptionServed: turn ran on the customer's own Anthropic/Codex
	// subscription token, so Weave charges nothing.
	SubscriptionServed bool
	// ByokServed: turn ran on the customer's own provider key; Weave charges
	// only the configured BYOK fee rate of the upstream cost rather than
	// full inference cost.
	ByokServed bool
	// APIKeyID attributes the debit to the authenticating key for
	// spend-cap tracking; empty leaves per-key spend untouched.
	APIKeyID string
	// RouterUserID attributes the debit to the resolved engineer identity for
	// monthly spend-limit tracking; empty leaves per-user spend untouched.
	RouterUserID string
}

// DebitForInference writes one ledger row at cost — no markup math here;
// margin is collected at top-up by the backend. Debits 0 for an override
// pass-through or subscription-served turn (already paid for), but always
// records NotionalCostMicros as a shadow trail.
//
// A BYOK turn debits 0 on the inference row (the customer paid their own
// provider) and adds a byok_fee row for the configured fee rate of the
// upstream cost (no fee row when the rate is zero).
// Override and subscription outrank BYOK.
//
// Returns the post-debit balance (0 on override, since balance doesn't
// change).
func (s *Service) DebitForInference(ctx context.Context, p DebitInferenceParams) (int64, error) {
	warnOnUnknownPricing(p)
	notional := computeNotionalMicros(p)
	delta := -notional
	var fee int64
	switch {
	case p.HasOverride, p.SubscriptionServed:
		// Already paid for — debit nothing, but still record notional cost below.
		delta = 0
	case p.ByokServed:
		// Customer paid their upstream directly; Weave charges only the fee.
		delta = 0
		fee = -s.byokFeeMicros(notional)
	}
	balanceAfter, err := s.repo.DebitInference(ctx, DebitParams{
		OrganizationID:     p.OrganizationID,
		DeltaUsdMicros:     delta,
		NotionalCostMicros: notional,
		EntryType:          EntryTypeInference,
		FeeUsdMicros:       fee,
		FeeEntryType:       EntryTypeByokFee,
		RouterRequestID:    p.RouterRequestID,
		RouterModel:        p.Model,
		APIKeyID:           p.APIKeyID,
		RouterUserID:       p.RouterUserID,
	})
	if err != nil {
		return balanceAfter, err
	}
	s.maybeSignalRecharge(ctx, p.OrganizationID, delta+fee, balanceAfter)
	return balanceAfter, nil
}

// maybeSignalRecharge fires once, on the debit that crosses the org's
// balance from at-or-above its autopay threshold to below it. No-ops if
// autopay isn't wired, the debit moved nothing, or autopay is disabled.
// A config-read error is logged and dropped (not returned) since the
// control-plane reconciliation sweep backstops a missed signal.
func (s *Service) maybeSignalRecharge(ctx context.Context, orgID string, delta, balanceAfter int64) {
	if s.autopay == nil || delta >= 0 {
		return
	}
	enabled, threshold, err := s.repo.GetAutopayConfig(ctx, orgID)
	if err != nil {
		observability.FromContext(ctx).Warn("Autopay crossing check skipped: config read failed",
			"organization_id", orgID, "err", err)
		return
	}
	if !enabled {
		return
	}
	// delta < 0, so the pre-debit balance is strictly greater than balanceAfter.
	balanceBefore := balanceAfter - delta
	if balanceBefore >= threshold && balanceAfter < threshold {
		s.autopay.NotifyRechargeNeeded(orgID)
	}
}

// warnOnUnknownPricing logs when a billable turn resolves to zero-value
// catalog pricing — the model ID has no internal/router/catalog.Models
// entry, so PrimaryPriceFor/PriceFor returned a zero Pricing and this turn
// is about to debit $0 for real usage. Override/subscription-served turns
// are exempt: those are intentionally free, not a pricing gap.
func warnOnUnknownPricing(p DebitInferenceParams) {
	if p.HasOverride || p.SubscriptionServed {
		return
	}
	if p.Pricing.InputUSDPer1M != 0 || p.Pricing.OutputUSDPer1M != 0 {
		return
	}
	if p.InputTokens == 0 && p.OutputTokens == 0 && p.CacheCreation == 0 && p.CacheRead == 0 {
		return
	}
	observability.Get().Error("Billing debit resolved zero-value catalog pricing for a real turn — add the model to internal/router/catalog/catalog.go's Models table",
		"model", p.Model,
		"provider", p.Provider,
		"organization_id", p.OrganizationID,
		"router_request_id", p.RouterRequestID,
		"input_tokens", p.InputTokens,
		"output_tokens", p.OutputTokens,
		"cache_creation_tokens", p.CacheCreation,
		"cache_read_tokens", p.CacheRead,
	)
}

// computeNotionalMicros returns the would-be charge in USD micros,
// regardless of override status, for the shadow billing trail.
func computeNotionalMicros(p DebitInferenceParams) int64 {
	inUSD := catalog.EffectiveInputCost(p.InputTokens, p.CacheCreation, p.CacheRead, p.Pricing.InputUSDPer1M, p.Pricing, p.Provider)
	outUSD := catalog.EffectiveOutputCost(p.OutputTokens, p.Pricing.OutputUSDPer1M)
	return catalog.USDToMicros(inUSD + outUSD)
}

// byokFeeMicros returns Weave's platform fee as a positive micros magnitude.
// Integer math avoids a float round-trip; rounds half away from zero so a
// sub-micro fee rounds up rather than disappearing.
func (s *Service) byokFeeMicros(notionalMicros int64) int64 {
	if notionalMicros <= 0 {
		return 0
	}
	const scale = 10_000 // the rate expressed as basis-points-of-a-basis-point
	rate := int64(s.byokFeeRate * scale)
	return (notionalMicros*rate + scale/2) / scale
}
