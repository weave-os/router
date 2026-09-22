package entitlement

import (
	"context"
	"errors"
	"fmt"
)

// ModelUnresolved records that a hold was filed before routing chose a model.
// Settlement names the served model on its own actions, so the hold does not
// need to guess one.
const ModelUnresolved = "unresolved"

// Hold is a pre-dispatch reservation of an upper-bound retail cost.
//
// UpperBoundUsdMicros is what the turn could cost at worst, not what it is
// expected to cost: the reservation exists so concurrent turns cannot each
// pass an admission check that only the first of them can afford, and an
// optimistic bound reopens exactly that gap. Finalization replaces it with the
// actual cost, so over-reserving only narrows the window between dispatch and
// settlement.
type Hold struct {
	Coverage            Coverage
	ActionID            string
	RouterRequestID     string
	APIKeyID            string
	ClientSessionID     string
	RequestedModel      string
	UpperBoundUsdMicros int64
	CapacitySource      CapacitySource
}

func (h Hold) reservation() Reservation {
	return Reservation{
		ActionID:              h.ActionID,
		RouterRequestID:       h.RouterRequestID,
		SubscriberID:          h.Coverage.SubscriberID,
		EntitlementVersion:    h.Coverage.EntitlementVersion,
		Plan:                  h.Coverage.Plan,
		BillingPeriod:         h.Coverage.BillingPeriod,
		WeeklyPeriod:          h.Coverage.WeeklyPeriod,
		SixHourPeriod:         h.Coverage.SixHourPeriod,
		APIKeyID:              h.APIKeyID,
		ClientSessionID:       h.ClientSessionID,
		RequestedModel:        h.RequestedModel,
		ReservedUsdMicros:     h.UpperBoundUsdMicros,
		CapacitySource:        h.CapacitySource,
		ReservedAt:            h.Coverage.AdmittedAt,
		BillingLimitUsdMicros: h.Coverage.BillingLimitUsdMicros,
		WeeklyLimitUsdMicros:  h.Coverage.WeeklyLimitUsdMicros,
		SixHourLimitUsdMicros: h.Coverage.SixHourLimitUsdMicros,
	}
}

// Reserve claims allowance for a turn before it is dispatched.
//
// It is the enforcement point, not Admit: Admit reads the windows, so any
// number of turns can read the same headroom and each conclude it fits. The
// reservation instead accrues and checks in one atomic write, which is what
// makes consumed + reserved <= limit hold under concurrency.
//
// A refusal reports ExhaustedError naming the window, and leaves no action
// behind: the turn was never dispatched, so nothing about it is billable. A
// redelivered action identifier returns the stored hold rather than claiming a
// second one.
func (s *Service) Reserve(ctx context.Context, hold Hold) (Action, error) {
	action, err := s.allowances.ReserveWithinLimits(ctx, hold.reservation())
	if err != nil {
		var exhausted ExhaustedError
		if errors.As(err, &exhausted) {
			return Action{}, err
		}
		return Action{}, fmt.Errorf("reserve subscriber allowance: %w", err)
	}
	return action, nil
}

// FinalizeHold settles a dispatched turn at its actual retail cost, returning
// the difference between the bound and the cost to the windows.
func (s *Service) FinalizeHold(ctx context.Context, actionID, servedModel string, retailUsdMicros int64, source CapacitySource) error {
	if _, err := s.allowances.Finalize(ctx, Finalization{
		ActionID:        actionID,
		ServedModel:     servedModel,
		RetailUsdMicros: retailUsdMicros,
		CapacitySource:  source,
		FinalizedAt:     s.now().UTC(),
	}); err != nil {
		return fmt.Errorf("finalize subscriber allowance hold: %w", err)
	}
	return nil
}

// ReleaseHold returns a hold that never became billable work — a failed
// dispatch, or a turn that ended up served by another capacity source.
//
// Leaving it standing would meter the subscriber for a turn they never got;
// releasing an already-finalized hold is refused by the store rather than
// reversing a real charge.
func (s *Service) ReleaseHold(ctx context.Context, actionID string) error {
	if _, err := s.allowances.Release(ctx, Release{ActionID: actionID, ReleasedAt: s.now().UTC()}); err != nil {
		return fmt.Errorf("release subscriber allowance hold: %w", err)
	}
	return nil
}
