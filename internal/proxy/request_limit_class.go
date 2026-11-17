package proxy

import (
	"context"
	"errors"
	"net/http"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/subscriptions/entitlement"
)

func requestLimitClass(err error) DispatchErrorClass {
	if errors.Is(err, policy.ErrRequestBodyTooLarge) {
		return DispatchErrorClass{
			Kind:       DispatchErrorRequestBodyTooLarge,
			Status:     http.StatusRequestEntityTooLarge,
			Message:    "This request is larger than any model available to it accepts. Start a new conversation or remove large attachments from the history.",
			LogLevel:   "warn",
			LogMessage: "Request body exceeds every eligible provider's size limit",
		}
	}
	return DispatchErrorClass{
		Kind:       DispatchErrorImageLimitExceeded,
		Status:     http.StatusBadRequest,
		Message:    "This request carries more images than any model available to it accepts in one request. Send fewer images (or start a new conversation without earlier screenshots).",
		LogLevel:   "warn",
		LogMessage: "Request image count exceeds every eligible model's limit",
	}
}

func requestBodyTooLargeFor(ctx context.Context, decision router.Decision) error {
	binding, ok := catalog.BindingFor(decision.Model, decision.Provider)
	if ok && !binding.AcceptsRequestBodyBytes(requestBodyBytesFromContext(ctx)) {
		return policy.ErrRequestBodyTooLarge
	}
	return nil
}

func dispatchPreconditions(ctx context.Context, in failoverInputs) error {
	if err := catalog.CheckEligibility(entitlement.ModelBoundaryFromContext(ctx), in.initialDecision.Model); err != nil {
		return err
	}
	if len(in.bindings) == 0 {
		return requestBodyTooLargeFor(ctx, in.initialDecision)
	}
	return nil
}

func primaryBindingWithinBodyLimit(ctx context.Context, decision router.Decision) []catalog.ProviderBinding {
	if requestBodyTooLargeFor(ctx, decision) != nil {
		return nil
	}
	return []catalog.ProviderBinding{{Provider: decision.Provider}}
}
