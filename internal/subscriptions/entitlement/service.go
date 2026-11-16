package entitlement

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type AdmissionOutcome string

const (
	AdmissionNotSubscribed AdmissionOutcome = "not_subscribed"
	AdmissionCovered       AdmissionOutcome = "covered"
	AdmissionExhausted     AdmissionOutcome = "exhausted"
)

type Coverage struct {
	SubscriberID          SubscriberID
	AdmittedAt            time.Time
	EntitlementVersion    int64
	Plan                  Plan
	BillingPeriod         Period
	WeeklyPeriod          Period
	SixHourPeriod         Period
	BillingLimitUsdMicros int64
	WeeklyLimitUsdMicros  int64
	SixHourLimitUsdMicros int64
	BillingUsedUsdMicros  int64
	WeeklyUsedUsdMicros   int64
	SixHourUsedUsdMicros  int64
	ProjectedUsdMicros    int64
}

type Admission struct {
	Outcome         AdmissionOutcome
	Plan            Plan
	Coverage        Coverage
	ExhaustedPeriod PeriodKind
	ResetsAt        time.Time
	Usage           Usage
}

type Service struct {
	entitlements EntitlementRepository
	allowances   AllowanceRepository
	now          func() time.Time
}

func NewService(entitlements EntitlementRepository, allowances AllowanceRepository) *Service {
	return &Service{entitlements: entitlements, allowances: allowances, now: time.Now}
}

func (s *Service) WithClock(now func() time.Time) *Service {
	s.now = now
	return s
}

func (s *Service) ProductScope(ctx context.Context, subscriberID SubscriberID) (Plan, error) {
	current, found, err := s.projectedEntitlement(ctx, subscriberID)
	if err != nil || !found {
		return "", err
	}
	return current.Plan, nil
}

func (s *Service) Admit(ctx context.Context, subscriberID SubscriberID) (Admission, error) {
	current, found, err := s.projectedEntitlement(ctx, subscriberID)
	if err != nil {
		return Admission{}, err
	}
	if !found {
		return Admission{Outcome: AdmissionNotSubscribed}, nil
	}
	at := s.now().UTC()
	if current.Status != StatusActive || !current.BillingPeriod.Covers(at) {
		return Admission{Outcome: AdmissionNotSubscribed, Plan: current.Plan}, nil
	}

	sixHour := SixHourWindowAt(at)
	weekly := WeeklyWindowAt(current.BillingPeriod, at)
	usage, err := s.allowances.Usage(ctx, subscriberID, current.BillingPeriod, weekly, sixHour)
	if err != nil {
		return Admission{}, fmt.Errorf("read subscriber allowance usage: %w", err)
	}
	weeklyLimit := WeeklyAllowanceUsdMicros(current.NominalMonthlyAllowanceUsdMicros, current.BillingPeriod, weekly)
	sixHourLimit := SixHourAllowanceUsdMicros(current.NominalMonthlyAllowanceUsdMicros, current.BillingPeriod, sixHour)
	usage.Billing.LimitUsdMicros = current.MonthlyAllowanceUsdMicros
	usage.Weekly.LimitUsdMicros = weeklyLimit
	usage.SixHour.LimitUsdMicros = sixHourLimit

	if exhausted, kind := spentWindow(usage); exhausted {
		var resetsAt time.Time
		for _, window := range []struct {
			usage WindowUsage
			end   time.Time
		}{{usage.Billing, current.BillingPeriod.End}, {usage.Weekly, weekly.End}, {usage.SixHour, sixHour.End}} {
			if window.usage.ConsumedUsdMicros() >= window.usage.LimitUsdMicros && window.end.After(resetsAt) {
				resetsAt = window.end
			}
		}
		return Admission{Outcome: AdmissionExhausted, Plan: current.Plan, ExhaustedPeriod: kind, ResetsAt: resetsAt, Usage: usage}, nil
	}
	return Admission{
		Outcome: AdmissionCovered,
		Plan:    current.Plan,
		Usage:   usage,
		Coverage: Coverage{
			SubscriberID:          subscriberID,
			AdmittedAt:            at,
			EntitlementVersion:    current.Version,
			Plan:                  current.Plan,
			BillingPeriod:         current.BillingPeriod,
			WeeklyPeriod:          weekly,
			SixHourPeriod:         sixHour,
			BillingLimitUsdMicros: current.MonthlyAllowanceUsdMicros,
			WeeklyLimitUsdMicros:  weeklyLimit,
			SixHourLimitUsdMicros: sixHourLimit,
			BillingUsedUsdMicros:  usage.Billing.ConsumedUsdMicros(),
			WeeklyUsedUsdMicros:   usage.Weekly.ConsumedUsdMicros(),
			SixHourUsedUsdMicros:  usage.SixHour.ConsumedUsdMicros(),
		},
	}, nil
}

func (s *Service) projectedEntitlement(ctx context.Context, subscriberID SubscriberID) (Entitlement, bool, error) {
	if !subscriberID.Valid() {
		return Entitlement{}, false, nil
	}
	current, err := s.entitlements.Get(ctx, subscriberID)
	if errors.Is(err, ErrEntitlementNotFound) {
		return Entitlement{}, false, nil
	}
	if err != nil {
		return Entitlement{}, false, fmt.Errorf("read subscriber entitlement: %w", err)
	}
	return current, true, nil
}

func spentWindow(usage Usage) (bool, PeriodKind) {
	if usage.Billing.ConsumedUsdMicros() >= usage.Billing.LimitUsdMicros {
		return true, PeriodKindBilling
	}
	if usage.Weekly.ConsumedUsdMicros() >= usage.Weekly.LimitUsdMicros {
		return true, PeriodKindWeekly
	}
	if usage.SixHour.ConsumedUsdMicros() >= usage.SixHour.LimitUsdMicros {
		return true, PeriodKindSixHour
	}
	return false, ""
}

type Settlement struct {
	Coverage        Coverage
	ActionID        string
	RouterRequestID string
	APIKeyID        string
	ClientSessionID string
	RequestedModel  string
	ServedModel     string
	RetailUsdMicros int64
	CapacitySource  CapacitySource
}

func (s *Service) Settle(ctx context.Context, settlement Settlement) error {
	at := s.now().UTC()
	reservation := Reservation{
		ActionID:              settlement.ActionID,
		RouterRequestID:       settlement.RouterRequestID,
		SubscriberID:          settlement.Coverage.SubscriberID,
		EntitlementVersion:    settlement.Coverage.EntitlementVersion,
		Plan:                  settlement.Coverage.Plan,
		BillingPeriod:         settlement.Coverage.BillingPeriod,
		WeeklyPeriod:          settlement.Coverage.WeeklyPeriod,
		SixHourPeriod:         settlement.Coverage.SixHourPeriod,
		APIKeyID:              settlement.APIKeyID,
		ClientSessionID:       settlement.ClientSessionID,
		RequestedModel:        settlement.RequestedModel,
		ReservedUsdMicros:     settlement.RetailUsdMicros,
		CapacitySource:        settlement.CapacitySource,
		ReservedAt:            settlement.Coverage.AdmittedAt,
		BillingLimitUsdMicros: settlement.Coverage.BillingLimitUsdMicros,
		WeeklyLimitUsdMicros:  settlement.Coverage.WeeklyLimitUsdMicros,
		SixHourLimitUsdMicros: settlement.Coverage.SixHourLimitUsdMicros,
	}
	action, err := s.allowances.Reserve(ctx, reservation)
	if err != nil {
		return fmt.Errorf("hold subscriber allowance: %w", err)
	}
	if action.State != ActionStateReserved {
		return nil
	}
	if _, err := s.allowances.Finalize(ctx, Finalization{
		ActionID:        settlement.ActionID,
		ServedModel:     settlement.ServedModel,
		RetailUsdMicros: settlement.RetailUsdMicros,
		CapacitySource:  settlement.CapacitySource,
		FinalizedAt:     at,
	}); err != nil {
		return fmt.Errorf("settle subscriber allowance (%v): %w", err, ErrAllowanceHeldUnsettled)
	}
	return nil
}
