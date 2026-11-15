package credentials

import (
	"context"
	"net/http"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
)

// Brokerage is the deployment's credential configuration, resolved at boot.
type Brokerage struct {
	// BYOKOnly disables deployment-level credential fallback so customer
	// requests never silently consume the platform's API key budget.
	BYOKOnly bool
	// KeyedProviders is the subset of registered providers whose upstream API
	// key is configured at the deployment level. When nil, all registered
	// providers are treated as deployment-keyed (legacy behavior).
	KeyedProviders map[string]struct{}
	// PassthroughEligible is the subset of registered providers reachable via
	// client-supplied auth headers (no deployment key, no BYOK). Surface-scoped:
	// only enabled when the inbound surface matches, otherwise an
	// Anthropic-surface `x-api-key` could forward to api.openai.com (and vice
	// versa) — a cross-provider credential leak.
	PassthroughEligible map[string]struct{}
	// ExcludedProvidersOverride, when non-nil, replaces the per-installation
	// provider exclusion list on every request. Set from
	// ROUTER_EXCLUDED_PROVIDERS at boot.
	ExcludedProvidersOverride map[string]struct{}
}

var _ Broker = Brokerage{}

// Eligibility applies deployment exclusions after enrollment; a BYOK gateway displaces every other provider.
func (b Brokerage) Eligibility(_ context.Context, req EligibilityRequest) Eligibility {
	excluded := b.ExcludedProviders(req.InstallationExcludedProviders, req.SessionDisabledProviders)
	return Eligibility{
		EnabledProviders:       b.enabledProviders(req, excluded),
		ExcludedProviders:      excluded,
		GatewayProviders:       GatewayProviders(req.ExternalKeys),
		SubscriptionProviders:  req.SubscriptionProviders,
		CustomBindings:         CustomBindings(req.ExternalKeys),
		ExcludedModels:         req.ExcludedModels,
		UsesNonDeploymentCreds: b.UsesNonDeploymentCreds(req.ExternalKeys, req.Headers),
	}
}

// EnabledProviders is Eligibility's EnabledProviders without the fields a
// caller that only needs the enabled set would discard.
func (b Brokerage) EnabledProviders(req EligibilityRequest) map[string]struct{} {
	return b.enabledProviders(req, b.ExcludedProviders(req.InstallationExcludedProviders, req.SessionDisabledProviders))
}

// UsesNonDeploymentCreds reports whether the request would use BYOK
// or client-supplied creds. The summarizer is wired with deployment-level
// creds, so calling it on a BYOK request would route conversation context
// through the platform account — the orchestrator skips the summarizer here.
func (b Brokerage) UsesNonDeploymentCreds(keys []*auth.ExternalAPIKey, headers http.Header) bool {
	if b.BYOKOnly {
		return true
	}
	if len(keys) > 0 {
		return true
	}
	// Scan every known provider (not a hand-maintained subset) so a newly-added
	// provider's client-supplied credential can't slip past the BYOK guard.
	for _, p := range providers.AllProviders() {
		if requestcontext.ExtractClientCredentials(p, headers) != nil {
			return true
		}
	}
	return false
}

// PolicyExcludedProviders returns configured exclusions only. Session
// strike-outs are omitted — transient 529 evidence must not veto a force
// the operator permits.
func (b Brokerage) PolicyExcludedProviders(installationExcluded []string) map[string]struct{} {
	if b.ExcludedProvidersOverride != nil {
		return b.ExcludedProvidersOverride
	}
	if len(installationExcluded) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(installationExcluded))
	for _, p := range installationExcluded {
		out[p] = struct{}{}
	}
	return out
}

// ExcludedProviders merges the deployment/installation exclusion list
// with any providers this session has struck out for repeated 529 exhaustion.
func (b Brokerage) ExcludedProviders(installationExcluded, sessionDisabled []string) map[string]struct{} {
	base := b.PolicyExcludedProviders(installationExcluded)
	if len(sessionDisabled) == 0 {
		return base
	}
	out := make(map[string]struct{}, len(base))
	for p := range base {
		out[p] = struct{}{}
	}
	for _, p := range sessionDisabled {
		out[p] = struct{}{}
	}
	return out
}

// enabledProviders returns providers with resolvable credentials
// for this request (deployment key, BYOK, client header, or linked subscription).
// A client-supplied bearer header is treated as creds for the surface only —
// never a licence to enable other OpenAI-compat upstreams sharing the same
// Authorization format.
func (b Brokerage) enabledProviders(req EligibilityRequest, excluded map[string]struct{}) map[string]struct{} {
	out := b.keyedProviders(req)
	enrollPassthrough(out, b.PassthroughEligible, req)
	for p := range req.SubscriptionLanes {
		out[p] = struct{}{}
	}
	enrollClientCredential(out, req)
	// Provider exclusions trump every enrollment path above: an excluded
	// provider must not be served even when credentials exist for it. The
	// scorer, hard-pin resolver, session pins, and tier clamp all consume
	// this set, so subtracting here enforces the exclusion everywhere a
	// routing decision is made.
	for p := range excluded {
		delete(out, p)
	}
	// A BYOK gateway is the tenant's own endpoint and displaces every other
	// upstream: routing through a vendor would send their traffic outside the
	// gateway they mandated. Applied last so no enrollment path above can
	// re-admit a vendor, and their provider exclusions become moot.
	if gateways := GatewayProviders(req.ExternalKeys); len(gateways) > 0 {
		return gateways
	}
	return out
}

// keyedProviders enrolls deployment keys, BYOK keys and inbound OAuth bearers.
func (b Brokerage) keyedProviders(req EligibilityRequest) map[string]struct{} {
	out := make(map[string]struct{}, len(req.Registered))
	if !b.BYOKOnly {
		if b.KeyedProviders != nil {
			for p := range b.KeyedProviders {
				out[p] = struct{}{}
			}
		} else {
			for p := range req.Registered {
				out[p] = struct{}{}
			}
		}
	}
	for _, k := range req.ExternalKeys {
		// Explicit failures stay eligible so selecting that provider returns
		// its credential error instead of using a deployment key.
		if len(k.Plaintext) == 0 && k.CredentialError == nil {
			continue
		}
		out[k.Provider] = struct{}{}
	}
	// A Claude subscription bearer (sk-ant-oat-) in the inbound
	// Authorization enrolls Anthropic even on router-keyed requests — Claude
	// Code keeps its OAuth token there while the router key rides in
	// X-Weave-Router-Key. OAuth-subset only: a general API key still can't
	// enroll a provider on the router-key path.
	if c := requestcontext.ExtractClientCredentials(providers.ProviderAnthropic, req.Headers); c != nil && c.OAuth {
		out[providers.ProviderAnthropic] = struct{}{}
	}
	// A Codex subscription bearer
	// in Authorization (paired with ChatGPT-Account-ID) enrolls OpenAI even on
	// router-keyed requests. OAuth-subset only: a plain API key still can't
	// enroll OpenAI on the router-key path.
	if c := requestcontext.ExtractClientCredentials(providers.ProviderOpenAI, req.Headers); c != nil && c.OAuth {
		out[providers.ProviderOpenAI] = struct{}{}
	}
	return out
}

func enrollPassthrough(out, passthroughEligible map[string]struct{}, req EligibilityRequest) {
	// Passthrough-eligible providers are surface-scoped: a provider without a
	// deployment key joins the eligible set only when the inbound surface
	// matches, else an Anthropic-surface `x-api-key` could leak to
	// api.openai.com (and vice versa). Skipped for router-key-authed requests
	// not already BYOK-enrolled: the inbound auth header IS the router key
	// there (stripped by setAuth), so dispatch would 401 unauthenticated
	// instead of failing fast with a 503.
	if req.SurfaceProvider != "" {
		if _, ok := passthroughEligible[req.SurfaceProvider]; ok {
			_, alreadyByok := out[req.SurfaceProvider]
			if !req.RouterKeyAuthed || alreadyByok {
				out[req.SurfaceProvider] = struct{}{}
			}
		}
	}
}

func enrollClientCredential(out map[string]struct{}, req EligibilityRequest) {
	// Client-supplied headers are only consulted when NOT authed via a
	// router key. A router-key-authed request carrying an inbound bearer
	// must not enable OpenAI-compat upstreams that share the Authorization
	// header format.
	if !req.RouterKeyAuthed && req.SurfaceProvider != "" {
		if _, already := out[req.SurfaceProvider]; !already {
			if requestcontext.ExtractClientCredentials(req.SurfaceProvider, req.Headers) != nil {
				out[req.SurfaceProvider] = struct{}{}
			}
		}
	}
}
