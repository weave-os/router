package proxy

import "weave-os/router/internal/refusal"

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
		Refusal:       &contract,
	}
}
