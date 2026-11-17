package proxy

import (
	"errors"
	"net/http"

	"weave-os/router/internal/router/policy"
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
