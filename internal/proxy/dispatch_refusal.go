package proxy

import (
	"weave-os/router/internal/ingress"
	"weave-os/router/internal/refusal"
)

func refusalClass(kind ingress.DispatchErrorKind, reason refusal.Kind, message, logLevel, logMessage string) ingress.DispatchErrorClass {
	contract := reason.Contract()
	return ingress.DispatchErrorClass{
		Kind:          kind,
		Status:        contract.Status,
		Message:       message,
		RetryAfter:    contract.RetryAfter,
		NoClientRetry: contract.NoClientRetry,
		LogLevel:      logLevel,
		LogMessage:    logMessage,
		Refusal:       &contract,
	}
}
