package credentials

import (
	"context"
	"net/http"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/subscriptions"
)

type Brokerage struct {
	BYOKOnly                  bool
	KeyedProviders            map[string]struct{}
	PassthroughEligible       map[string]struct{}
	ExcludedProvidersOverride map[string]struct{}
	SubscriptionModels        SubscriptionModelAccess
	CodexExtraCredits         bool
	ManagedSubscriptions      subscriptions.Leaser
	SessionHolds              SubscriptionSessionHolds
	ClaudePlans               *subscriptions.PlanResolver
	UsageObserver             *usage.Observer
}

var _ Broker = (*Brokerage)(nil)

func (b *Brokerage) Eligibility(_ context.Context, req EligibilityRequest) Eligibility {
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

func (b *Brokerage) EnabledProviders(req EligibilityRequest) map[string]struct{} {
	return b.enabledProviders(req, b.ExcludedProviders(req.InstallationExcludedProviders, req.SessionDisabledProviders))
}

func (b *Brokerage) UsesNonDeploymentCreds(keys []*auth.ExternalAPIKey, headers http.Header) bool {
	if b.BYOKOnly {
		return true
	}
	if len(keys) > 0 {
		return true
	}
	for _, p := range providers.AllProviders() {
		if requestcontext.ExtractClientCredentials(p, headers) != nil {
			return true
		}
	}
	return false
}

func (b *Brokerage) PolicyExcludedProviders(installationExcluded []string) map[string]struct{} {
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

func (b *Brokerage) ExcludedProviders(installationExcluded, sessionDisabled []string) map[string]struct{} {
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

func (b *Brokerage) enabledProviders(req EligibilityRequest, excluded map[string]struct{}) map[string]struct{} {
	out := b.keyedProviders(req)
	enrollPassthrough(out, b.PassthroughEligible, req)
	for p := range req.SubscriptionLanes {
		out[p] = struct{}{}
	}
	enrollClientCredential(out, req)
	for p := range excluded {
		delete(out, p)
	}
	if gateways := GatewayProviders(req.ExternalKeys); len(gateways) > 0 {
		return gateways
	}
	return out
}

func (b *Brokerage) keyedProviders(req EligibilityRequest) map[string]struct{} {
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
		if len(k.Plaintext) == 0 && k.CredentialError == nil {
			continue
		}
		out[k.Provider] = struct{}{}
	}
	if c := requestcontext.ExtractClientCredentials(providers.ProviderAnthropic, req.Headers); c != nil && c.OAuth {
		out[providers.ProviderAnthropic] = struct{}{}
	}
	if c := requestcontext.ExtractClientCredentials(providers.ProviderOpenAI, req.Headers); c != nil && c.OAuth {
		out[providers.ProviderOpenAI] = struct{}{}
	}
	return out
}

func enrollPassthrough(out, passthroughEligible map[string]struct{}, req EligibilityRequest) {
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
	if !req.RouterKeyAuthed && req.SurfaceProvider != "" {
		if _, already := out[req.SurfaceProvider]; !already {
			if requestcontext.ExtractClientCredentials(req.SurfaceProvider, req.Headers) != nil {
				out[req.SurfaceProvider] = struct{}{}
			}
		}
	}
}
