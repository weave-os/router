package proxy

import "weave-os/router/internal/refusal"

// refusalClass classifies a dispatch-time refusal, taking its status and retry
// signals from the refusal table.
func refusalClass(kind DispatchErrorKind, reason refusal.Kind, message, logLevel, logMessage string) DispatchErrorClass {
	contract := reason.Contract()
	return DispatchErrorClass{
		Kind:          kind,
		Status:        contract.Status,
		Message:       message,
		RetryAfter:    contract.RetryAfter,
		NoClientRetry: contract.NoClientRetry,
		LogLevel:      logLevel,
		LogMessage:    logMessage,
	}
}
