package proxy

import (
	"errors"
	"net/http"

	"weave-os/router/internal/ingress"
	"weave-os/router/internal/router/policy"
)

func routingEligibilityClass(err error) ingress.DispatchErrorClass {
	if errors.Is(err, policy.ErrNoRoutableModels) {
		return ingress.DispatchErrorClass{
			Kind:       ingress.DispatchErrorNoRoutableModels,
			Status:     http.StatusBadRequest,
			Message:    "No model is routable for your organization: the current model and provider selection leaves nothing this request can run on. Ask your org admin to widen it.",
			LogLevel:   "warn",
			LogMessage: "Installation configuration leaves no routable model",
		}
	}
	return ingress.DispatchErrorClass{
		Kind:          ingress.DispatchErrorNoEligibleArm,
		Status:        http.StatusBadRequest,
		Message:       "No model can serve this request: every model the routing policy could pick is ruled out for this turn, by the request's provider requirements or by models that already failed this turn. Retrying the same request will not help.",
		NoClientRetry: true,
		LogLevel:      "warn",
		LogMessage:    "Routing policy has no eligible arm for this request",
	}
}
