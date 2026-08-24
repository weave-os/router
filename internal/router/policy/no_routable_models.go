package policy

import (
	"errors"
	"fmt"
)

// ErrNoRoutableModels signals that resolution came back empty because of the
// installation's own configuration, not a router fault. It exists so the
// dispatch layer can answer "your configuration routes nowhere" instead of
// reporting the policy router as unavailable.
var ErrNoRoutableModels = errors.New("policy: configuration leaves no routable model")

// ErrGatewayServesNoDeployedModel is ErrNoRoutableModels narrowed to the
// gateway-exclusive case: the installation routes through its own gateway, and
// no deployed model is named by a gateway key's aliases — typically a key saved
// with no aliases at all, which serves nothing.
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
