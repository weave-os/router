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

// emptyCandidateError names why resolution produced no candidate, so the
// caller reports the configuration that has to change rather than a generic
// routing failure.
func emptyCandidateError(diagnostics []Diagnostic) error {
	for _, diagnostic := range diagnostics {
		if diagnostic.Reason != ExclusionGatewayNotServed {
			return ErrNoRoutableModels
		}
	}
	if len(diagnostics) == 0 {
		return ErrNoRoutableModels
	}
	return ErrGatewayServesNoDeployedModel
}
