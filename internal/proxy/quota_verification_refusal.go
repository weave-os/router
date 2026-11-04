package proxy

import (
	"context"
	"errors"
	"net/http"

	"weave-os/router/internal/providers"
)

const quotaVerificationUnavailableMessage = "Your subscription's included quota couldn't be verified right now, so this turn wasn't served and paid fallback stayed off. Retry in a moment."

var quotaVerificationUnavailableClass = DispatchErrorClass{
	Kind:       DispatchErrorQuotaVerificationUnavailable,
	Status:     http.StatusServiceUnavailable,
	Message:    quotaVerificationUnavailableMessage,
	RetryAfter: true,
	LogLevel:   "warn",
	LogMessage: "Subscription-only request refused: included quota could not be verified",
}

// quotaVerificationControlsRefusal reports whether an unverifiable quota
// preflight is the only subscription outcome behind a refusal. A funding
// sentinel joined to it says paid fallback was off, not that the plan ran
// out, so the client must retry rather than top up. Any other upstream
// outcome (exhaustion, auth, inference failure) is authoritative and keeps
// its own classification.
func quotaVerificationControlsRefusal(err error) bool {
	verificationUnavailable, authoritative := quotaVerificationOutcomes(err)
	return verificationUnavailable && !authoritative
}

func quotaVerificationOutcomes(err error) (verificationUnavailable, authoritative bool) {
	if err == nil {
		return false, false
	}
	if quotaVerificationCauseOf(err) {
		return true, false
	}
	if isUpstreamOutcome(err) || errors.Is(err, context.Canceled) {
		return false, true
	}
	var children []error
	switch wrapped := err.(type) { //nolint:errorlint // inspects this node only; errors.As would see through to nested causes
	case interface{ Unwrap() []error }:
		children = wrapped.Unwrap()
	case interface{ Unwrap() error }:
		children = []error{wrapped.Unwrap()}
	}
	for _, child := range children {
		childUnavailable, childAuthoritative := quotaVerificationOutcomes(child)
		verificationUnavailable = verificationUnavailable || childUnavailable
		authoritative = authoritative || childAuthoritative
	}
	return verificationUnavailable, authoritative
}

// quotaVerificationCauseOf reports whether err itself is the verification
// failure or the upstream response carrying it, not a join that merely
// contains one.
func quotaVerificationCauseOf(err error) bool {
	switch typed := err.(type) { //nolint:errorlint // inspects this node only; errors.As would see through to nested causes
	case *providers.QuotaVerificationUnavailableError:
		return true
	case *providers.UpstreamErrorResponse:
		_, ok := typed.Cause.(*providers.QuotaVerificationUnavailableError) //nolint:errorlint // inspects this node only; errors.As would see through to nested causes
		return ok
	case *providers.UpstreamStatusError:
		return quotaVerificationCauseOf(typed.Cause)
	}
	return false
}

func isUpstreamOutcome(err error) bool {
	switch err.(type) { //nolint:errorlint // inspects this node only; errors.As would see through to nested causes
	case *providers.UpstreamErrorResponse, *providers.UpstreamStatusError:
		return true
	}
	return false
}
