package router

import (
	"errors"
	"fmt"
)

var ErrNoEligibleProvider = errors.New("router: no eligible provider for request")

var ErrAllowlistEmptiesPool = fmt.Errorf("router: model allowlist leaves no eligible candidates: %w", ErrNoEligibleProvider)

var ErrInvalidRoutingKnobs = errors.New("router: invalid routing knobs")

var ErrRoutingUnavailable = errors.New("router: routing unavailable")

var ErrRLPolicyUnavailable = errors.New("rl: policy router unavailable")

var ErrHMMUnavailable = errors.New("hmm: policy router unavailable")

var ErrDecisionModelUnavailable = errors.New("decision model classifier unavailable")
