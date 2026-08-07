package proxy

import (
	"context"
	"net/http"
)

// Skip reasons reported by gateSummarizerCall, used as log fields so an
// operator can tell a policy refusal apart from a tenant-boundary one.
const (
	summarizerSkipProviderExcluded = "provider_excluded"
	summarizerSkipModelExcluded    = "model_excluded"
	summarizerSkipTenantBoundary   = "tenant_boundary"
)

// summarizerGate is the resolved verdict for one synthetic summarizer call:
// whether it may run at all, and under whose credentials.
type summarizerGate struct {
	// Creds are the caller's own forwarded credentials for the summarizer's
	// provider, or nil to run on the deployment key.
	Creds *Credentials
	// Allowed reports whether the call may be dispatched.
	Allowed bool
	// SkipReason is one of the summarizerSkip* constants when Allowed is false.
	SkipReason string
}

// gateSummarizerCall decides whether the summary of a conversation may be sent
// to provider/model for this request.
//
// A summary call ships the entire prior conversation upstream, so it is subject
// to the same exclusions as routed traffic: a provider or model the operator
// excluded must not receive the content it was excluded from seeing. Session
// strike-outs are deliberately not consulted — transient 529 evidence is not a
// policy statement, matching policyExcludedProviders.
//
// The tenant boundary is unchanged: prefer the caller's own forwarded
// credentials, and rather than spend the deployment key on a BYOK/client-keyed
// request, skip.
func (s *Service) gateSummarizerCall(ctx context.Context, provider, model string, headers http.Header) summarizerGate {
	if _, excluded := s.policyExcludedProviders(ctx)[provider]; excluded {
		return summarizerGate{SkipReason: summarizerSkipProviderExcluded}
	}
	if model != "" {
		if _, excluded := s.excludedModelsForRequest(ctx)[model]; excluded {
			return summarizerGate{SkipReason: summarizerSkipModelExcluded}
		}
	}
	creds := resolveSummarizerCreds(ctx, provider, headers)
	if creds == nil && s.requestUsesNonDeploymentCreds(ctx, headers) {
		return summarizerGate{SkipReason: summarizerSkipTenantBoundary}
	}
	return summarizerGate{Creds: creds, Allowed: true}
}

// summarizerContext returns the context the summary call runs under: the
// caller's own credentials when the gate resolved some, otherwise a context
// stripped of any request credential (e.g. a subscription OAuth token) so the
// synthetic call runs on the deployment key instead of inheriting one that
// would 401 or cross a tenant boundary.
func (g summarizerGate) summarizerContext(ctx context.Context) context.Context {
	if g.Creds != nil {
		return context.WithValue(ctx, CredentialsContextKey{}, g.Creds)
	}
	return clearCredentials(ctx)
}
