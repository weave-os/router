package policy

import (
	"context"
	"errors"
	"fmt"
)

// FailureReason distinguishes classifier faults from invalid inference requests.
type FailureReason string

const (
	FailureEvidence    FailureReason = "evidence_unavailable"
	FailureTransport   FailureReason = "transport"
	FailureTimeout     FailureReason = "timeout"
	FailureOverload    FailureReason = "overload"
	FailureAuth        FailureReason = "authentication"
	FailureContract    FailureReason = "contract"
	FailureNoRankedArm FailureReason = "no_ranked_arm"
	FailureCircuitOpen FailureReason = "circuit_open"
	FailureCanceled    FailureReason = "caller_canceled"
)

// DependencyError describes an internal classifier failure; Status is never a
// statement about the validity of the caller's inference request.
type DependencyError struct {
	Reason FailureReason
	Status int
	Cause  error
}

func (e *DependencyError) Error() string {
	description := fmt.Sprintf("policy dependency %s", e.Reason)
	if e.Status != 0 {
		description += fmt.Sprintf(" (status %d)", e.Status)
	}
	if e.Cause != nil {
		return fmt.Sprintf("%s: %v", description, e.Cause)
	}
	return description
}

func (e *DependencyError) Unwrap() error { return e.Cause }

// FailureReasonFor preserves typed dependency diagnoses through retry wrappers.
func FailureReasonFor(err error) FailureReason {
	var failure *DependencyError
	if errors.As(err, &failure) {
		return failure.Reason
	}
	if errors.Is(err, ErrNoEligibleArm) {
		return FailureNoRankedArm
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return FailureTimeout
	}
	if errors.Is(err, context.Canceled) {
		return FailureCanceled
	}
	return FailureContract
}
