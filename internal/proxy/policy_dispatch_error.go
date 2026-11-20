package proxy

import (
	"errors"
	"net/http"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/hmm"
)

func classifyPolicyDispatchError(err error) (DispatchErrorClass, bool) {
	switch {
	case errors.Is(err, router.ErrRLPolicyUnavailable):
		return DispatchErrorClass{
			Kind:       DispatchErrorRLPolicyUnavailable,
			Status:     http.StatusServiceUnavailable,
			Message:    "Router unavailable: RL policy router failed and no fallback is configured.",
			RetryAfter: true,
			LogLevel:   "error",
			LogMessage: "RL routing unavailable",
		}, true
	case errors.Is(err, router.ErrDecisionModelUnavailable):
		return DispatchErrorClass{
			Kind:       DispatchErrorDecisionModelUnavailable,
			Status:     http.StatusServiceUnavailable,
			Message:    "Router unavailable: decision model routing failed and no fallback is configured.",
			RetryAfter: true,
			LogLevel:   "error",
			LogMessage: "decision model routing unavailable",
		}, true
	case errors.Is(err, hmm.ErrHMMUnavailable):
		return DispatchErrorClass{
			Kind:       DispatchErrorHMMUnavailable,
			Status:     http.StatusServiceUnavailable,
			Message:    "Router unavailable: HMM policy router failed and no fallback is configured.",
			RetryAfter: true,
			LogLevel:   "error",
			LogMessage: "HMM routing unavailable",
		}, true
	case errors.Is(err, router.ErrStrategyUnavailable):
		return DispatchErrorClass{
			Kind:       DispatchErrorPolicyUnavailable,
			Status:     http.StatusServiceUnavailable,
			Message:    "Router unavailable: selected policy router is not configured.",
			RetryAfter: true,
			LogLevel:   "error",
			LogMessage: "Policy routing unavailable",
		}, true
	case errors.Is(err, router.ErrRoutingUnavailable):
		return DispatchErrorClass{
			Kind:       DispatchErrorClusterUnavailable,
			Status:     http.StatusServiceUnavailable,
			Message:    "Router unavailable: no routing policy could serve this request.",
			RetryAfter: true,
			LogLevel:   "error",
			LogMessage: "Routing unavailable",
		}, true
	default:
		return DispatchErrorClass{}, false
	}
}
