package entitlement

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// AdmissionOutcome is the enforcement verdict for one inbound request.
type AdmissionOutcome string

const (
	// AdmissionNotSubscribed means no individual entitlement covers the caller,
	// so the request keeps whatever billing path it already had.
	AdmissionNotSubscribed AdmissionOutcome = "not_subscribed"
	// AdmissionCovered means the included Router allowance has headroom.
	AdmissionCovered AdmissionOutcome = "covered"
	// AdmissionExhausted means an enforcement window is spent.
	AdmissionExhausted AdmissionOutcome = "exhausted"
)

// Coverage is the entitlement state a covered request accrues against. It is
// captured at admission so settlement accrues to the windows the request was
// admitted under, even when a projection lands mid-flight.
type Coverage struct {
	SubscriberID SubscriberID
	// AdmittedAt is the reservation clock for the turn: the windows below are
	// the ones covering it, so a turn that serves past a window boundary still
	// accrues where admission read it.
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

// Admission is the verdict plus the usage that produced it.
type Admission struct {
	Outcome AdmissionOutcome
	// Plan is the subscriber's plan, empty for AdmissionNotSubscribed. It is
	// reported for an exhausted allowance too: the plan's hard product boundary
	// governs the turn whether or not the included allowance is paying for it.
	Plan Plan
	// Coverage is populated for AdmissionCovered only.
	Coverage Coverage
	// ExhaustedPeriod names the spent window for AdmissionExhausted.
	ExhaustedPeriod PeriodKind
	Usage           Usage
}

// HeldCapacityOnly reports that the limiting windows would have headroom
// without in-flight reservations. A subscriber whose finalized usage actually
// reached a limit can still use their organization's normal payment fallback.
func (a Admission) HeldCapacityOnly() bool {
	if a.Outcome != AdmissionExhausted {
		return false
	}
	held := false
	for _, window := range []WindowUsage{a.Usage.Billing, a.Usage.Weekly, a.Usage.SixHour} {
		if window.FinalizedUsdMicros >= window.LimitUsdMicros {
			return false
		}
		if window.ConsumedUsdMicros() >= window.LimitUsdMicros {
			held = true
		}
	}
	return held
}

// Service admits requests against a subscriber's included Router allowance and
// settles their actual retail cost.
type Service struct {
	entitlements EntitlementRepository
	allowances   AllowanceRepository
	now          func() time.Time
}

// NewService binds enforcement to the durable projection and accounting stores.
func NewService(entitlements EntitlementRepository, allowances AllowanceRepository) *Service {
	return &Service{entitlements: entitlements, allowances: allowances, now: time.Now}
}

// WithClock overrides the enforcement clock; tests derive deterministic windows.
func (s *Service) WithClock(now func() time.Time) *Service {
	s.now = now
	return s
}

// Admit reports whether the subscriber's included allowance can take a request.
//
// A caller with no projected entitlement, a non-active one, or one whose
// projected billing period no longer covers now is reported as not subscribed
// rather than exhausted: those requests are paid for by the org/prepaid paths
// that already gate them, and a stale projection must not hand out free usage.
//
// The month's allowance is the projected one, which Weave prorates across the
// entitlement segments covering the period. The weekly and six-hour caps are
// derived here instead: they depend on the window being admitted, not only on
// the entitlement, so they cannot be projected scalars.
//
// Read failures are surfaced, never swallowed — an allowance that admits
// everything on a database error is an unbilled-usage hole.
//
// Admission is the only gate, so turns already in flight when a window fills
// still settle and carry it slightly past its allowance; the next admission
// then sees the overshoot and rejects. Held cost counts as consumed, so the
// overshoot is bounded by the concurrent turns in flight and never compounds —
// the same bound a prepaid balance has on debits in flight when it hits zero.
func (s *Service) Admit(ctx context.Context, subscriberID SubscriberID) (Admission, error) {
	if !subscriberID.Valid() {
		return Admission{Outcome: AdmissionNotSubscribed}, nil
	}
	current, err := s.entitlements.Get(ctx, subscriberID)
	if errors.Is(err, ErrEntitlementNotFound) {
		return Admission{Outcome: AdmissionNotSubscribed}, nil
	}
	if err != nil {
		return Admission{}, fmt.Errorf("read subscriber entitlement: %w", err)
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
		return Admission{Outcome: AdmissionExhausted, Plan: current.Plan, ExhaustedPeriod: kind, Usage: usage}, nil
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

// spentWindow reports the first exhausted enforcement window, longest first:
// an exhausted month is reported as such even when the week and the six-hour
// window inside it are also spent, because the remediation differs — a top-up
// or renewal for the month, the turn of the clock for the shorter windows.
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

// Settlement records one served action's actual retail cost against the
// coverage the request was admitted under.
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

// Settle books an action's retail cost against the subscriber's windows.
//
// Cost is only known once a turn has served, so the hold and the settlement are
// written back to back for the same action identifier: the hold establishes the
// durable, idempotent action row, and finalization moves it to actual cost. A
// duplicate delivery of the same action identifier is absorbed by the
// repository rather than double-charging.
//
// The hold is filed at the coverage's admission clock, not at settlement time:
// a turn that serves past a window boundary belongs to the windows admission
// read it against, and those are the windows the coverage carries.
//
// A hold whose finalization fails is left standing on purpose. It holds exactly
// the retail cost the turn incurred, and held and settled amounts count against
// the window alike, so the subscriber is metered correctly either way; only the
// served-model audit detail is lost. That failure reports
// ErrAllowanceHeldUnsettled so a caller falling back to another book can tell
// it apart from a turn the allowance never recorded.
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
		// Already settled or returned by an earlier delivery of this action.
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
