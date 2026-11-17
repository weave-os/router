package proxy

import (
	"net/http"

	"weave-os/router/internal/router/policy"
)

func classifyResolutionError(err *policy.ResolutionError) DispatchErrorClass {
	switch err.Code {
	case policy.ResolutionErrorInvalidOverride, policy.ResolutionErrorOverrideNotAllowed, policy.ResolutionErrorDuplicateOverride, policy.ResolutionErrorUnknownSelection, policy.ResolutionErrorBudgetViolation:
		if err.Source == policy.OverrideSourceRequest || err.Code == policy.ResolutionErrorUnknownSelection {
			return DispatchErrorClass{
				Kind:       DispatchErrorPlanOverrideRejected,
				Status:     http.StatusBadRequest,
				Message:    "The requested model, provider, or budget is not permitted for this operation.",
				LogLevel:   "warn",
				LogMessage: "Inference plan rejected request-supplied override",
			}
		}
	case policy.ResolutionErrorUnknownPurpose, policy.ResolutionErrorUnsupportedPurpose, policy.ResolutionErrorMissingSelection, policy.ResolutionErrorNoEligibleBinding, policy.ResolutionErrorConstraintViolation:
	}
	return DispatchErrorClass{
		Kind:       DispatchErrorPlanUnresolvable,
		Status:     http.StatusServiceUnavailable,
		Message:    "Router unavailable: no inference target could be authorized for this request.",
		RetryAfter: true,
		LogLevel:   "error",
		LogMessage: "Inference plan resolution failed",
	}
}
