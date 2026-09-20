// Package entitlement defines subscriber subscription and allowance contracts.
package entitlement

import (
	"context"
	"errors"
	"time"
)

var (
	// ErrInvalidContract means a subscription value failed domain validation.
	ErrInvalidContract = errors.New("invalid subscription entitlement contract")
	// ErrEntitlementNotFound means no projected entitlement exists for a subscriber.
	ErrEntitlementNotFound = errors.New("subscriber entitlement not found")
	// ErrStaleProjection means a projection version is older than the stored version.
	ErrStaleProjection = errors.New("stale subscriber entitlement projection")
	// ErrProjectionConflict means the same version was projected with different values.
	ErrProjectionConflict = errors.New("conflicting subscriber entitlement projection")
	// ErrAllowanceActionNotFound means no durable action exists for an action identifier.
	ErrAllowanceActionNotFound = errors.New("subscriber allowance action not found")
	// ErrAllowanceActionConflict means a settlement command contradicts the stored action.
	ErrAllowanceActionConflict = errors.New("conflicting subscriber allowance action")
	// ErrAllowanceExhausted means a pre-dispatch reservation was refused
	// because the window it would accrue against has no headroom left.
	ErrAllowanceExhausted = errors.New("subscriber allowance exhausted")
	// ErrAllowanceHeldUnsettled means a settlement failed after its hold was
	// durably recorded. The windows already count the turn's cost, so a caller
	// falling back to another book must not charge it a second time.
	ErrAllowanceHeldUnsettled = errors.New("subscriber allowance hold left unsettled")
)

// ExhaustedError names the enforcement window that refused a reservation, so
// a caller can tell a spent six-hour window (retry after the window turns)
// from a spent billing month (retry after renewal or a top-up).
type ExhaustedError struct {
	Period PeriodKind
}

func (e ExhaustedError) Error() string {
	return "subscriber allowance exhausted: " + string(e.Period) + " window"
}

func (e ExhaustedError) Unwrap() error { return ErrAllowanceExhausted }

// SubscriberID is the opaque credential-subject identity authenticated by Router.
type SubscriberID string

// Valid reports whether the subscriber identity is present.
func (id SubscriberID) Valid() bool {
	return id != ""
}

// Plan identifies an individual Router subscription product.
type Plan string

const (
	// PlanMax is the Max individual subscription.
	PlanMax Plan = "max"
	// PlanBoost is the Boost individual subscription.
	PlanBoost Plan = "boost"
)

// Valid reports whether the plan is recognized.
func (p Plan) Valid() bool {
	return p == PlanMax || p == PlanBoost
}

// Status is the projected commerce lifecycle state.
type Status string

const (
	// StatusCheckoutPending means checkout has not completed.
	StatusCheckoutPending Status = "checkout_pending"
	// StatusActive means the subscription is paid and active.
	StatusActive Status = "active"
	// StatusPastDue means payment remediation is required.
	StatusPastDue Status = "past_due"
	// StatusCanceled means cancellation is scheduled or effective.
	StatusCanceled Status = "canceled"
	// StatusEnded means the subscription no longer grants access.
	StatusEnded Status = "ended"
)

// Valid reports whether the status is recognized.
func (s Status) Valid() bool {
	switch s {
	case StatusCheckoutPending, StatusActive, StatusPastDue, StatusCanceled, StatusEnded:
		return true
	default:
		return false
	}
}

// CapacitySource identifies the account that served an action.
type CapacitySource string

const (
	// CapacitySourceIncludedRouter uses the subscriber's included Router allowance.
	CapacitySourceIncludedRouter CapacitySource = "included_router"
	// CapacitySourceLinkedClaude uses a linked Claude subscription.
	CapacitySourceLinkedClaude CapacitySource = "linked_claude"
	// CapacitySourceLinkedCodex uses a linked Codex subscription.
	CapacitySourceLinkedCodex CapacitySource = "linked_codex"
	// CapacitySourcePrepaid uses subscriber-owned prepaid Router funds.
	CapacitySourcePrepaid CapacitySource = "prepaid"
	// CapacitySourceBillingOverride uses an explicit billing override.
	CapacitySourceBillingOverride CapacitySource = "billing_override"
)

// Valid reports whether the capacity source is recognized.
func (s CapacitySource) Valid() bool {
	switch s {
	case CapacitySourceIncludedRouter, CapacitySourceLinkedClaude, CapacitySourceLinkedCodex, CapacitySourcePrepaid, CapacitySourceBillingOverride:
		return true
	default:
		return false
	}
}

// PeriodKind distinguishes billing periods from fixed six-hour windows.
type PeriodKind string

const (
	// PeriodKindBilling is the projected commerce billing period.
	PeriodKindBilling PeriodKind = "billing"
	// PeriodKindSixHour is a fixed UTC six-hour allowance window.
	PeriodKindSixHour PeriodKind = "six_hour"
)

// Valid reports whether the period kind is recognized.
func (k PeriodKind) Valid() bool {
	return k == PeriodKindBilling || k == PeriodKindSixHour
}

// Period identifies one allowance accounting interval.
type Period struct {
	Kind  PeriodKind
	Start time.Time
	End   time.Time
}

// Validate rejects unknown, empty, or non-UTC intervals.
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
	return nil
}

// Allowance is the durable aggregate for one subscriber period.
type Allowance struct {
	SubscriberID       SubscriberID
	EntitlementVersion int64
	Plan               Plan
	Period             Period
	LimitUsdMicros     int64
	ReservedUsdMicros  int64
	FinalizedUsdMicros int64
}

// Validate rejects malformed allowance aggregates.
func (a Allowance) Validate() error {
	if !a.SubscriberID.Valid() || a.EntitlementVersion <= 0 || !a.Plan.Valid() || a.LimitUsdMicros < 0 || a.ReservedUsdMicros < 0 || a.FinalizedUsdMicros < 0 {
		return ErrInvalidContract
	}
	return a.Period.Validate()
}

// Entitlement is the current versioned subscription projection for one subscriber.
type Entitlement struct {
	SubscriberID  SubscriberID
	Version       int64
	Plan          Plan
	Status        Status
	BillingPeriod Period
	EffectiveAt   time.Time
	// MonthlyAllowanceUsdMicros is the period allowance Weave prorated across
	// the entitlement segments covering the billing period; the nominal one is
	// the plan's undivided monthly figure, which the window caps derive from.
	MonthlyAllowanceUsdMicros        int64
	NominalMonthlyAllowanceUsdMicros int64
	// SixHourAllowanceUsdMicros is Weave's period-average window figure, kept
	// for display. Enforcement uses SixHourAllowanceUsdMicros(), which resolves
	// the cap of the specific window being admitted.
	SixHourAllowanceUsdMicros int64
	AutoTopUpEnabled          bool
	ProjectedAt               time.Time
}

// Validate rejects malformed entitlement projections.
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

// WindowUsage is the consumed amount of one subscriber allowance window.
type WindowUsage struct {
	Period             Period
	LimitUsdMicros     int64
	ReservedUsdMicros  int64
	FinalizedUsdMicros int64
}

// ConsumedUsdMicros counts held and settled retail cost against the window.
func (u WindowUsage) ConsumedUsdMicros() int64 {
	return u.ReservedUsdMicros + u.FinalizedUsdMicros
}

// Usage reports consumption of both enforcement windows covering one request.
type Usage struct {
	Billing WindowUsage
	SixHour WindowUsage
}

// ActionState is the durable lifecycle of an allowance accounting action.
type ActionState string

const (
	// ActionStateReserved means an upper-bound retail cost is held.
	ActionStateReserved ActionState = "reserved"
	// ActionStateFinalized means actual catalog-derived retail cost is recorded.
	ActionStateFinalized ActionState = "finalized"
	// ActionStateReleased means a non-billable reservation was returned.
	ActionStateReleased ActionState = "released"
)

// Valid reports whether the action state is recognized.
func (s ActionState) Valid() bool {
	return s == ActionStateReserved || s == ActionStateFinalized || s == ActionStateReleased
}

// Reservation requests an idempotent upper-bound allowance hold.
type Reservation struct {
	ActionID           string
	RouterRequestID    string
	SubscriberID       SubscriberID
	EntitlementVersion int64
	Plan               Plan
	BillingPeriod      Period
	SixHourPeriod      Period
	APIKeyID           string
	ClientSessionID    string
	RequestedModel     string
	ReservedUsdMicros  int64
	CapacitySource     CapacitySource
	ReservedAt         time.Time
	// Window limits seed the accounting periods the hold accrues against, so a
	// mid-period plan change lands with the reservation that observed it.
	BillingLimitUsdMicros int64
	SixHourLimitUsdMicros int64
}

// Validate rejects malformed reservation commands.
func (r Reservation) Validate() error {
	if r.ActionID == "" || r.RouterRequestID == "" || !r.SubscriberID.Valid() || r.EntitlementVersion <= 0 ||
		!r.Plan.Valid() || r.APIKeyID == "" || r.RequestedModel == "" || r.ReservedUsdMicros < 0 ||
		!r.CapacitySource.Valid() || r.ReservedAt.IsZero() || !isUTC(r.ReservedAt) ||
		r.BillingLimitUsdMicros < 0 || r.SixHourLimitUsdMicros < 0 ||
		r.BillingPeriod.Kind != PeriodKindBilling || r.SixHourPeriod.Kind != PeriodKindSixHour {
		return ErrInvalidContract
	}
	if err := r.BillingPeriod.Validate(); err != nil {
		return err
	}
	if err := r.SixHourPeriod.Validate(); err != nil {
		return err
	}
	// The period starts are the aggregate keys the hold accrues against, so a
	// reservation filed outside the windows containing it would draw down one
	// window while admission reads another.
	if !r.BillingPeriod.Covers(r.ReservedAt) || r.SixHourPeriod != SixHourWindowAt(r.ReservedAt) {
		return ErrInvalidContract
	}
	return nil
}

// Finalization records the actual retail cost and serving source for a reserved action.
type Finalization struct {
	ActionID        string
	ServedModel     string
	RetailUsdMicros int64
	CapacitySource  CapacitySource
	FinalizedAt     time.Time
}

// Validate rejects malformed finalization commands.
func (f Finalization) Validate() error {
	if f.ActionID == "" || f.ServedModel == "" || f.RetailUsdMicros < 0 || !f.CapacitySource.Valid() || f.FinalizedAt.IsZero() || !isUTC(f.FinalizedAt) {
		return ErrInvalidContract
	}
	return nil
}

// Release identifies a non-billable reservation to return.
type Release struct {
	ActionID   string
	ReleasedAt time.Time
}

// Validate rejects malformed release commands.
func (r Release) Validate() error {
	if r.ActionID == "" || r.ReleasedAt.IsZero() || !isUTC(r.ReleasedAt) {
		return ErrInvalidContract
	}
	return nil
}

// Action is the durable audit record for a reservation and its outcome.
type Action struct {
	Reservation
	State           ActionState
	ServedModel     string
	RetailUsdMicros int64
	FinalizedAt     *time.Time
	ReleasedAt      *time.Time
}

// Validate rejects malformed durable action states.
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

// EntitlementRepository stores the current monotonic projection per subscriber.
type EntitlementRepository interface {
	Project(context.Context, Entitlement) error
	Get(context.Context, SubscriberID) (Entitlement, error)
}

// AllowanceRepository stores idempotent reservation, finalization, and release actions.
type AllowanceRepository interface {
	Reserve(context.Context, Reservation) (Action, error)
	// ReserveWithinLimits holds a reservation only while both enforcement
	// windows can still pay for it, and returns ExhaustedError naming the
	// window that refused otherwise. The hold and both window accruals are one
	// atomic unit: a refused window leaves no action and no partial draw-down.
	ReserveWithinLimits(context.Context, Reservation) (Action, error)
	Finalize(context.Context, Finalization) (Action, error)
	Release(context.Context, Release) (Action, error)
	Usage(ctx context.Context, subscriberID SubscriberID, billing, sixHour Period) (Usage, error)
}
