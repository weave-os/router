package proxy

import (
	"context"
	"errors"

	"weave-os/router/internal/credentials"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
)

const maxQuotaUnverifiedRetries = 2

func quotaUnverified(err error) bool {
	var unavailable *providers.QuotaVerificationUnavailableError
	return errors.As(err, &unavailable)
}

func (l *accountLoop) retryUnverifiedQuotaOnSameLogin(ctx, credentialCtx context.Context, attemptErr error) bool {
	if !credentials.ServedOnCodexSubscription(credentialCtx) || !quotaUnverified(attemptErr) || l.quotaUnverifiedRetries >= maxQuotaUnverifiedRetries {
		return false
	}
	l.quotaUnverifiedRetries++
	observability.FromContext(ctx).Info("Codex quota could not be verified; retrying the same login",
		"model", l.decision.Model, "provider", l.decision.Provider, "quota_unverified_retry", l.quotaUnverifiedRetries)
	return true
}
