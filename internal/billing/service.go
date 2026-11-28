package billing

import (
	"context"
	"errors"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/subscriptions/entitlement"
)

type hasOverrideContextKeyT struct{}

var HasOverrideContextKey = hasOverrideContextKeyT{}

func HasOverrideFromContext(ctx context.Context) bool {
	v := ctx.Value(HasOverrideContextKey)
	if v == nil {
		return false
	}
	b, _ := v.(bool)
	return b
}

type organizationPrepaidContextKeyT struct{}

func WithOrganizationPrepaid(ctx context.Context) context.Context {
	return context.WithValue(ctx, organizationPrepaidContextKeyT{}, true)
}

func OrganizationPrepaidFromContext(ctx context.Context) bool {
	admitted, _ := ctx.Value(organizationPrepaidContextKeyT{}).(bool)
	return admitted
}

const EntryTypeInference = "inference"

const EntryTypeByokFee = "byok_fee"

const DefaultByokFeeRate = 0.0

const MinBalanceMicros int64 = 0

type Service struct {
	repo           Repo
	autopay        AutopayNotifier
	byokFeeRate    float64
	allowances     SubscriberAllowanceSettler
	prepaid        *PrepaidBooks
	delegatedSpend DelegatedSpendReader
	admission      AdmissionReader
}

func NewService(repo Repo) *Service {
	books := newPrepaidBooks()
	books.register(OwnerKindOrganization, OrganizationBook(repo))
	return &Service{repo: repo, byokFeeRate: DefaultByokFeeRate, prepaid: books}
}

func (s *Service) PrepaidBalance(ctx context.Context, owner Owner) (int64, error) {
	return s.prepaid.Balance(ctx, owner)
}

func (s *Service) DebitPrepaid(ctx context.Context, debit PrepaidDebit) (int64, error) {
	return s.prepaid.Debit(ctx, debit)
}

func (s *Service) WithByokFeeRate(rate float64) *Service {
	if rate < 0 {
		rate = 0
	}
	s.byokFeeRate = rate
	return s
}

type AutopayNotifier interface {
	NotifyRechargeNeeded(owner Owner)
}

func (s *Service) WithAutopayNotifier(n AutopayNotifier) *Service {
	s.autopay = n
	return s
}

type SubscriberAllowanceSettler interface {
	Settle(context.Context, entitlement.Settlement) error
}

func (s *Service) WithSubscriberAllowance(settler SubscriberAllowanceSettler) *Service {
	s.allowances = settler
	return s
}

type CheckResult struct {
	HasOverride   bool
	BalanceMicros int64
}

type APIKeySpendCapResult struct {
	Found       bool
	SpentMicros int64
	CapMicros   *int64
}

var ErrDelegatedSpendUnavailable = errors.New("delegated spend reader not configured")

func (s *Service) WithDelegatedSpend(reader DelegatedSpendReader) *Service {
	s.delegatedSpend = reader
	return s
}

func (s *Service) CheckDelegatedSpend(ctx context.Context, issuerKeyID, sessionID, taskID string) (sessionSpentMicros, taskSpentMicros int64, err error) {
	if s.delegatedSpend == nil {
		return 0, 0, ErrDelegatedSpendUnavailable
	}
	return s.delegatedSpend.GetDelegatedTokenSpend(ctx, issuerKeyID, sessionID, taskID)
}

func (s *Service) CheckAPIKeySpendCap(ctx context.Context, apiKeyID string) (APIKeySpendCapResult, error) {
	spent, cap, found, err := s.repo.GetAPIKeySpend(ctx, apiKeyID)
	if err != nil {
		return APIKeySpendCapResult{}, err
	}
	return APIKeySpendCapResult{Found: found, SpentMicros: spent, CapMicros: cap}, nil
}

func (s *Service) CheckBalance(ctx context.Context, orgID string) (CheckResult, error) {
	override, err := s.repo.HasActiveOverride(ctx, orgID)
	if err != nil {
		return CheckResult{}, err
	}
	if override {
		return CheckResult{HasOverride: true}, nil
	}
	balance, err := s.PrepaidBalance(ctx, OrganizationOwner(orgID))
	if err != nil {
		return CheckResult{}, err
	}
	return CheckResult{BalanceMicros: balance}, nil
}

type MonthlySpendResult struct {
	SpentMicros   int64
	LimitMicros   *int64
	GrantedMicros int64
}

func (r MonthlySpendResult) EffectiveLimitMicros() *int64 {
	if r.LimitMicros == nil {
		return nil
	}
	effective := *r.LimitMicros + r.GrantedMicros
	return &effective
}

func (r MonthlySpendResult) LimitReached() bool {
	effective := r.EffectiveLimitMicros()
	return effective != nil && r.SpentMicros >= *effective
}

func (s *Service) CheckUserMonthlySpend(ctx context.Context, organizationID, routerUserID string) (MonthlySpendResult, error) {
	spent, limit, granted, err := s.repo.GetUserMonthlySpendAndLimit(ctx, organizationID, routerUserID)
	if err != nil {
		return MonthlySpendResult{}, err
	}
	return MonthlySpendResult{SpentMicros: spent, LimitMicros: limit, GrantedMicros: granted}, nil
}

func (s *Service) CheckOrgMonthlySpend(ctx context.Context, organizationID string) (MonthlySpendResult, error) {
	spent, limit, err := s.repo.GetOrgMonthlySpendAndLimit(ctx, organizationID)
	if err != nil {
		return MonthlySpendResult{}, err
	}
	return MonthlySpendResult{SpentMicros: spent, LimitMicros: limit}, nil
}

type DebitInferenceParams struct {
	OrganizationID     string
	RouterRequestID    string
	Model              string
	Provider           string
	InputTokens        int
	OutputTokens       int
	CacheCreation      int
	CacheRead          int
	Pricing            catalog.Pricing
	UsageModifiers     catalog.UsageModifiers
	HasOverride        bool
	SubscriptionServed bool
	ByokServed         bool
	APIKeyID           string
	DelegatedSessionID string
	DelegatedTaskID    string
	RouterUserID       string
	RequestedModel     string
}

func (p DebitInferenceParams) requestedModel() string {
	if p.RequestedModel != "" {
		return p.RequestedModel
	}
	return p.Model
}

func (s *Service) DebitForInference(ctx context.Context, p DebitInferenceParams) (int64, error) {
	warnOnUnknownPricing(p)
	notional := computeNotionalMicros(p)
	if owner, ok := InternalTestOwnerFrom(ctx); ok {
		return s.debitInternalTest(ctx, p, owner.TestSubjectID, notional)
	}
	coverage, hasCoverage := entitlement.CoverageFromContext(ctx)
	subscriberCovered := hasCoverage && s.allowances != nil && !p.HasOverride && !p.SubscriptionServed && !p.ByokServed

	delta := -notional
	var fee int64
	switch {
	case p.HasOverride, p.SubscriptionServed:
		delta = 0
	case p.ByokServed:
		delta = 0
		fee = -s.byokFeeMicros(notional)
	case subscriberCovered:
		held, err := s.meterSubscriberAllowance(ctx, p, coverage, notional)
		if err != nil {
			return 0, err
		}
		if !held {
			return 0, errors.New("subscriber allowance settlement recorded no hold")
		}
		delta = 0
	}
	balanceAfter, err := s.DebitPrepaid(ctx, PrepaidDebit{
		Owner:              OrganizationOwner(p.OrganizationID),
		DeltaUsdMicros:     delta,
		NotionalCostMicros: notional,
		EntryType:          EntryTypeInference,
		FeeUsdMicros:       fee,
		FeeEntryType:       EntryTypeByokFee,
		RouterRequestID:    p.RouterRequestID,
		RouterModel:        p.Model,
		APIKeyID:           p.APIKeyID,
		RouterUserID:       p.RouterUserID,
		DelegatedSessionID: p.DelegatedSessionID,
		DelegatedTaskID:    p.DelegatedTaskID,
	})
	if err != nil {
		return balanceAfter, err
	}
	s.maybeSignalRecharge(ctx, OrganizationOwner(p.OrganizationID), delta+fee, balanceAfter)
	return balanceAfter, nil
}

func (s *Service) meterSubscriberAllowance(ctx context.Context, p DebitInferenceParams, coverage entitlement.Coverage, retailMicros int64) (bool, error) {
	actionID, ok := entitlement.NextActionID(ctx, p.RouterRequestID)
	if !ok {
		entitlement.MarkSettlementFailed(ctx)
		return false, errors.New("subscriber allowance coverage has no action binding")
	}
	err := s.allowances.Settle(ctx, entitlement.Settlement{
		Coverage:        coverage,
		ActionID:        actionID,
		RouterRequestID: p.RouterRequestID,
		APIKeyID:        p.APIKeyID,
		RequestedModel:  p.requestedModel(),
		ServedModel:     p.Model,
		RetailUsdMicros: retailMicros,
		CapacitySource:  entitlement.CapacitySourceIncludedRouter,
	})
	if err == nil {
		return true, nil
	}
	held := errors.Is(err, entitlement.ErrAllowanceHeldUnsettled)
	observability.FromContext(ctx).Error("Subscriber allowance settlement failed",
		"err", err,
		"hold_stands", held,
		"subscriber_id", string(coverage.SubscriberID),
		"router_request_id", p.RouterRequestID,
		"retail_usd_micros", retailMicros,
	)
	if held {
		return true, err
	}
	entitlement.MarkSettlementFailed(ctx)
	return false, err
}

func (s *Service) maybeSignalRecharge(ctx context.Context, owner Owner, delta, balanceAfter int64) {
	if s.autopay == nil || delta >= 0 {
		return
	}
	enabled, threshold, err := s.repo.GetAutopayConfig(ctx, owner)
	if err != nil {
		log := observability.FromContext(ctx).With("owner_kind", owner.Kind)
		if owner.Kind == OwnerKindOrganization {
			log = log.With("organization_id", owner.OrganizationID)
		}
		log.Warn("Autopay crossing check skipped: config read failed", "err", err)
		return
	}
	if !enabled {
		return
	}
	balanceBefore := balanceAfter - delta
	if balanceBefore >= threshold && balanceAfter < threshold {
		s.autopay.NotifyRechargeNeeded(owner)
	}
}

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

func computeNotionalMicros(p DebitInferenceParams) int64 {
	inUSD := catalog.EffectiveInputCost(p.InputTokens, p.CacheCreation, p.CacheRead, p.Pricing, p.Provider, p.UsageModifiers)
	outUSD := catalog.EffectiveOutputCost(catalog.PromptTokens(p.InputTokens, p.CacheCreation, p.CacheRead, p.Provider), p.OutputTokens, p.Pricing, p.UsageModifiers)
	return catalog.USDToMicros(inUSD + outUSD)
}

func (s *Service) byokFeeMicros(notionalMicros int64) int64 {
	if notionalMicros <= 0 {
		return 0
	}
	const scale = 10_000
	rate := int64(s.byokFeeRate * scale)
	return (notionalMicros*rate + scale/2) / scale
}
