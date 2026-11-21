package proxy

import (
	"errors"
	"net/http"

	"weave-os/router/internal/ingress"
	"weave-os/router/internal/router"
)

func classifyPolicyDispatchError(err error) (ingress.DispatchErrorClass, bool) {
	switch {
	case errors.Is(err, router.ErrRLPolicyUnavailable):
		return ingress.DispatchErrorClass{
			Kind:       ingress.DispatchErrorRLPolicyUnavailable,
			Status:     http.StatusServiceUnavailable,
			Message:    "Router unavailable: RL policy router failed and no fallback is configured.",
			RetryAfter: true,
			LogLevel:   "error",
			LogMessage: "RL routing unavailable",
		}, true
	case errors.Is(err, router.ErrDecisionModelUnavailable):
		return ingress.DispatchErrorClass{
			Kind:       ingress.DispatchErrorDecisionModelUnavailable,
			Status:     http.StatusServiceUnavailable,
			Message:    "Router unavailable: decision model routing failed and no fallback is configured.",
			RetryAfter: true,
			LogLevel:   "error",
			LogMessage: "decision model routing unavailable",
		}, true
	case errors.Is(err, router.ErrHMMUnavailable):
		return ingress.DispatchErrorClass{
			Kind:       ingress.DispatchErrorHMMUnavailable,
			Status:     http.StatusServiceUnavailable,
			Message:    "Router unavailable: HMM policy router failed and no fallback is configured.",
			RetryAfter: true,
			LogLevel:   "error",
			LogMessage: "HMM routing unavailable",
		}, true
	case errors.Is(err, router.ErrStrategyUnavailable):
		return ingress.DispatchErrorClass{
			Kind:       ingress.DispatchErrorPolicyUnavailable,
			Status:     http.StatusServiceUnavailable,
			Message:    "Router unavailable: selected policy router is not configured.",
			RetryAfter: true,
			LogLevel:   "error",
			LogMessage: "Policy routing unavailable",
		}, true
	case errors.Is(err, router.ErrRoutingUnavailable):
		return ingress.DispatchErrorClass{
			Kind:       ingress.DispatchErrorClusterUnavailable,
			Status:     http.StatusServiceUnavailable,
			Message:    "Router unavailable: no routing policy could serve this request.",
			RetryAfter: true,
			LogLevel:   "error",
			LogMessage: "Routing unavailable",
		}, true
	default:
		return ingress.DispatchErrorClass{}, false
	}
}
