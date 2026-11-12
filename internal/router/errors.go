package router

import (
	"errors"
	"fmt"
)

// ErrNoEligibleProvider is returned when no deployed candidate survives the
// request's enabled providers and exclusions. Callers map to HTTP 4xx.
var ErrNoEligibleProvider = errors.New("router: no eligible provider for request")

// ErrAllowlistEmptiesPool wraps ErrNoEligibleProvider so existing errors.Is
// checks keep matching, while naming the allowlist instead of the desugared exclusion list.
var ErrAllowlistEmptiesPool = fmt.Errorf("router: model allowlist leaves no eligible candidates: %w", ErrNoEligibleProvider)

// ErrInvalidRoutingKnobs is returned when effective routing knobs fail validation.
var ErrInvalidRoutingKnobs = errors.New("router: invalid routing knobs")

// ErrRoutingUnavailable is returned when no routing policy can produce a
// decision for the request. Callers map to HTTP 503.
var ErrRoutingUnavailable = errors.New("router: routing unavailable")
