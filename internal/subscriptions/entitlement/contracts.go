package entitlement

import (
	"context"
	"errors"
	"time"
)

var (
	ErrInvalidContract         = errors.New("invalid subscription entitlement contract")
	ErrEntitlementNotFound     = errors.New("subscriber entitlement not found")
	ErrStaleProjection         = errors.New("stale subscriber entitlement projection")
	ErrProjectionConflict      = errors.New("conflicting subscriber entitlement projection")
	ErrAllowanceActionNotFound = errors.New("subscriber allowance action not found")
	ErrAllowanceActionConflict = errors.New("conflicting subscriber allowance action")
	ErrAllowanceHeldUnsettled  = errors.New("subscriber allowance hold left unsettled")
)

const ModelUnresolved = "unresolved"

type SubscriberID string

func (id SubscriberID) Valid() bool {
	return id != ""
}

type Plan string

const (
	PlanMax   Plan = "max"
	PlanBoost Plan = "boost"
)

func (p Plan) Valid() bool {
	return p == PlanMax || p == PlanBoost
}

type Status string

const (
	StatusCheckoutPending Status = "checkout_pending"
	StatusActive          Status = "active"
	StatusPastDue         Status = "past_due"
	StatusCanceled        Status = "canceled"
	StatusEnded           Status = "ended"
)

func (s Status) Valid() bool {
	switch s {
	case StatusCheckoutPending, StatusActive, StatusPastDue, StatusCanceled, StatusEnded:
		return true
	default:
		return false
	}
}

type CapacitySource string

const (
	CapacitySourceIncludedRouter  CapacitySource = "included_router"
	CapacitySourceLinkedClaude    CapacitySource = "linked_claude"
	CapacitySourceLinkedCodex     CapacitySource = "linked_codex"
	CapacitySourcePrepaid         CapacitySource = "prepaid"
	CapacitySourceBillingOverride CapacitySource = "billing_override"
)

func (s CapacitySource) Valid() bool {
	switch s {
	case CapacitySourceIncludedRouter, CapacitySourceLinkedClaude, CapacitySourceLinkedCodex, CapacitySourcePrepaid, CapacitySourceBillingOverride:
		return true
	default:
		return false
	}
}

type PeriodKind string

const (
	PeriodKindBilling PeriodKind = "billing"
	PeriodKindWeekly  PeriodKind = "weekly"
	PeriodKindSixHour PeriodKind = "six_hour"
)

func (k PeriodKind) Valid() bool {
	return k == PeriodKindBilling || k == PeriodKindWeekly || k == PeriodKindSixHour
}

type Period struct {
	Kind  PeriodKind
	Start time.Time
	End   time.Time
}

func (p Period) Validate() error {
	if !p.Kind.Valid() || p.Start.IsZero() || p.End.IsZero() || !p.Start.Before(p.End) || !isUTC(p.Start) || !isUTC(p.End) {
		return ErrInvalidContract
	}
	if p.Kind == PeriodKindSixHour && p.End.Sub(p.Start) != 6*time.Hour {
		return ErrInvalidContract
	}
	if p.Kind == PeriodKindSixHour && (p.Start.Hour()%6 != 0 || p.Start.Minute() != 0 || p.Start.Second() != 0 || p.Start.Nanosecond() != 0) {
		return ErrInvalidContract
	}
	if p.Kind == PeriodKindWeekly && p.End.Sub(p.Start) > weeklyWindow {
		return ErrInvalidContract
	}
	return nil
}

type Allowance struct {
	SubscriberID       SubscriberID
	EntitlementVersion int64
	Plan               Plan
	Period             Period
	LimitUsdMicros     int64
	ReservedUsdMicros  int64
	FinalizedUsdMicros int64
}

func (a Allowance) Validate() error {
	if !a.SubscriberID.Valid() || a.EntitlementVersion <= 0 || !a.Plan.Valid() || a.LimitUsdMicros < 0 || a.ReservedUsdMicros < 0 || a.FinalizedUsdMicros < 0 {
		return ErrInvalidContract
	}
	return a.Period.Validate()
}

type Entitlement struct {
	SubscriberID                     SubscriberID
	Version                          int64
	Plan                             Plan
	Status                           Status
	BillingPeriod                    Period
	EffectiveAt                      time.Time
	MonthlyAllowanceUsdMicros        int64
	NominalMonthlyAllowanceUsdMicros int64
	SixHourAllowanceUsdMicros        int64
	AutoTopUpEnabled                 bool
	ProjectedAt                      time.Time
}

func (e Entitlement) Validate() error {
	if !e.SubscriberID.Valid() || e.Version <= 0 || !e.Plan.Valid() || !e.Status.Valid() ||
		e.MonthlyAllowanceUsdMicros < 0 || e.NominalMonthlyAllowanceUsdMicros < 0 || e.SixHourAllowanceUsdMicros < 0 ||
		e.EffectiveAt.IsZero() || e.ProjectedAt.IsZero() || !isUTC(e.EffectiveAt) || !isUTC(e.ProjectedAt) {
		return ErrInvalidContract
	}
	if e.BillingPeriod.Kind != PeriodKindBilling {
		return ErrInvalidContract
	}
	return e.BillingPeriod.Validate()
}

type WindowUsage struct {
	Period             Period
	LimitUsdMicros     int64
	ReservedUsdMicros  int64
	FinalizedUsdMicros int64
}

func (u WindowUsage) ConsumedUsdMicros() int64 {
	return u.ReservedUsdMicros + u.FinalizedUsdMicros
}

type Usage struct {
	Billing WindowUsage
	Weekly  WindowUsage
	SixHour WindowUsage
}

type ActionState string

const (
	ActionStateReserved  ActionState = "reserved"
	ActionStateFinalized ActionState = "finalized"
	ActionStateReleased  ActionState = "released"
)

func (s ActionState) Valid() bool {
	return s == ActionStateReserved || s == ActionStateFinalized || s == ActionStateReleased
}

type Reservation struct {
	ActionID              string
	RouterRequestID       string
	SubscriberID          SubscriberID
	EntitlementVersion    int64
	Plan                  Plan
	BillingPeriod         Period
	WeeklyPeriod          Period
	SixHourPeriod         Period
	APIKeyID              string
	ClientSessionID       string
	RequestedModel        string
	ReservedUsdMicros     int64
	CapacitySource        CapacitySource
	ReservedAt            time.Time
	BillingLimitUsdMicros int64
	WeeklyLimitUsdMicros  int64
	SixHourLimitUsdMicros int64
}

func (r Reservation) Validate() error {
	if r.ActionID == "" || r.RouterRequestID == "" || !r.SubscriberID.Valid() || r.EntitlementVersion <= 0 ||
		!r.Plan.Valid() || r.APIKeyID == "" || r.RequestedModel == "" || r.ReservedUsdMicros < 0 ||
		!r.CapacitySource.Valid() || r.ReservedAt.IsZero() || !isUTC(r.ReservedAt) ||
		r.BillingLimitUsdMicros < 0 || r.WeeklyLimitUsdMicros < 0 || r.SixHourLimitUsdMicros < 0 ||
		r.BillingPeriod.Kind != PeriodKindBilling || r.WeeklyPeriod.Kind != PeriodKindWeekly ||
		r.SixHourPeriod.Kind != PeriodKindSixHour {
		return ErrInvalidContract
	}
	if err := r.BillingPeriod.Validate(); err != nil {
		return err
	}
	if err := r.WeeklyPeriod.Validate(); err != nil {
		return err
	}
	if err := r.SixHourPeriod.Validate(); err != nil {
		return err
	}
	if !r.BillingPeriod.Covers(r.ReservedAt) || r.SixHourPeriod != SixHourWindowAt(r.ReservedAt) ||
		r.WeeklyPeriod != WeeklyWindowAt(r.BillingPeriod, r.ReservedAt) {
		return ErrInvalidContract
	}
	return nil
}

type Finalization struct {
	ActionID        string
	ServedModel     string
	RetailUsdMicros int64
	CapacitySource  CapacitySource
	FinalizedAt     time.Time
}

func (f Finalization) Validate() error {
	if f.ActionID == "" || f.ServedModel == "" || f.RetailUsdMicros < 0 || !f.CapacitySource.Valid() || f.FinalizedAt.IsZero() || !isUTC(f.FinalizedAt) {
		return ErrInvalidContract
	}
	return nil
}

type Release struct {
	ActionID   string
	ReleasedAt time.Time
}

func (r Release) Validate() error {
	if r.ActionID == "" || r.ReleasedAt.IsZero() || !isUTC(r.ReleasedAt) {
		return ErrInvalidContract
	}
	return nil
}

type Action struct {
	Reservation
	State           ActionState
	ServedModel     string
	RetailUsdMicros int64
	FinalizedAt     *time.Time
	ReleasedAt      *time.Time
}

func (a Action) Validate() error {
	if err := a.Reservation.Validate(); err != nil || !a.State.Valid() || a.RetailUsdMicros < 0 {
		return ErrInvalidContract
	}
	switch a.State {
	case ActionStateReserved:
		if a.ServedModel != "" || a.RetailUsdMicros != 0 || a.FinalizedAt != nil || a.ReleasedAt != nil {
			return ErrInvalidContract
		}
	case ActionStateFinalized:
		if a.ServedModel == "" || !validUTCTime(a.FinalizedAt) || a.FinalizedAt.Before(a.ReservedAt) || a.ReleasedAt != nil {
			return ErrInvalidContract
		}
	case ActionStateReleased:
		if a.ServedModel != "" || a.RetailUsdMicros != 0 || a.FinalizedAt != nil || !validUTCTime(a.ReleasedAt) || a.ReleasedAt.Before(a.ReservedAt) {
			return ErrInvalidContract
		}
	}
	return nil
}

func validUTCTime(value *time.Time) bool {
	return value != nil && !value.IsZero() && isUTC(*value)
}

func isUTC(value time.Time) bool {
	return value.Location() == time.UTC
}

type EntitlementRepository interface {
	Project(context.Context, Entitlement) error
	Get(context.Context, SubscriberID) (Entitlement, error)
}

type AllowanceRepository interface {
	Reserve(context.Context, Reservation) (Action, error)
	Finalize(context.Context, Finalization) (Action, error)
	Release(context.Context, Release) (Action, error)
	Usage(ctx context.Context, subscriberID SubscriberID, billing, weekly, sixHour Period) (Usage, error)
}
