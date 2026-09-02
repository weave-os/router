package policy

import (
	"errors"
	"fmt"
)

// ErrNoRoutableModels signals that resolution came back empty due to the
// installation's own configuration, not a router fault.
var ErrNoRoutableModels = errors.New("policy: configuration leaves no routable model")

// ErrGatewayServesNoDeployedModel is ErrNoRoutableModels narrowed to the
// gateway-exclusive case: no deployed model is aliased by any gateway key.
var ErrGatewayServesNoDeployedModel = fmt.Errorf(
	"policy: no deployed model is aliased by a gateway key: %w", ErrNoRoutableModels)

// NoRoutableModelsError preserves the safe resolver diagnostics that explain
// why a policy candidate pool became empty. It wraps one of the policy
// sentinels so existing HTTP classification remains unchanged.
type NoRoutableModelsError struct {
	Diagnostics []Diagnostic
	Cause       error
}

// Error returns the policy sentinel's stable message.
func (e *NoRoutableModelsError) Error() string {
	if e.Cause == nil {
		return ErrNoRoutableModels.Error()
	}
	return e.Cause.Error()
}

// Unwrap preserves errors.Is checks against the existing policy sentinels.
func (e *NoRoutableModelsError) Unwrap() error {
	if e.Cause == nil {
		return ErrNoRoutableModels
	}
	return e.Cause
}

// emptyCandidateError names why resolution produced no candidate, so the
// caller reports the configuration that has to change rather than a generic
// routing failure.
func emptyCandidateError(diagnostics []Diagnostic) error {
	cause := ErrNoRoutableModels
	for _, diagnostic := range diagnostics {
		if diagnostic.Reason != ExclusionGatewayNotServed {
			return &NoRoutableModelsError{Diagnostics: diagnostics, Cause: cause}
		}
	}
	if len(diagnostics) > 0 {
		cause = ErrGatewayServesNoDeployedModel
	}
	return &NoRoutableModelsError{Diagnostics: diagnostics, Cause: cause}
}
