package proxy

import (
	"context"
	"net/http"
	"sort"

	"workweave/router/internal/providers"
	"workweave/router/internal/router"
	"workweave/router/internal/router/catalog"
)

func isCodexProvider(provider string) bool {
	return provider == providers.ProviderOpenAI || provider == providers.ProviderCodex
}

// annotateDecisionCredentialSource attaches non-secret credential provenance
// to a routing decision after the provider and model have been selected.
func (s *Service) annotateDecisionCredentialSource(ctx context.Context, headers http.Header, decision router.Decision) router.Decision {
	decision = codexDecisionCredentialSource(decision)
	if decision.CredentialSource != router.CredentialSourceUnknown {
		return decision
	}
	if creds := CredentialsFromContext(ctx); creds != nil {
		decision.CredentialSource = credentialSourceForCredentials(creds)
		return decision
	}
	if s.usesDeploymentCredential(decision.Provider) {
		decision.CredentialSource = router.CredentialSourceDeploymentKey
		return decision
	}
	if client := ExtractClientCredentials(decision.Provider, headers); client != nil {
		decision.CredentialSource = credentialSourceForCredentials(client)
	}
	return decision
}

func credentialSourceForCredentials(creds *Credentials) router.CredentialSource {
	if creds == nil {
		return router.CredentialSourceUnknown
	}
	switch creds.Source {
	case credSourceBYOK:
		return router.CredentialSourceBYOK
	case credSourceCodexSubscription:
		return router.CredentialSourceCodexOAuth
	case credSourceSubscription:
		return router.CredentialSourceClaudeOAuth
	case credSourceClient:
		return router.CredentialSourceClientAPIKey
	default:
		return router.CredentialSourceUnknown
	}
}

func (s *Service) usesDeploymentCredential(provider string) bool {
	if provider == "" || s.byokOnly {
		return false
	}
	if s.deploymentKeyedProviders == nil {
		_, registered := s.providers[provider]
		return registered
	}
	_, keyed := s.deploymentKeyedProviders[provider]
	return keyed
}

// credentialBindingsForRequest returns non-secret credential constraints for
// candidate resolution. Provider-level availability remains in
// EnabledProviders; this adds the model/endpoint scope that a provider-wide
// set cannot express, notably Codex OAuth's native Responses-only family.
func (s *Service) credentialBindingsForRequest(ctx context.Context, headers http.Header, enabled map[string]struct{}) []router.CredentialBinding {
	if len(enabled) == 0 {
		return nil
	}

	byok := BuildCredentialsMap(externalKeysFromContext(ctx))
	codexToken, _ := presentSubscriptionTokens(ctx, headers)
	codexAvailable := codexToken != ""
	providersInRequest := make([]string, 0, len(enabled))
	for provider := range enabled {
		providersInRequest = append(providersInRequest, provider)
	}
	sort.Strings(providersInRequest)

	bindings := make([]router.CredentialBinding, 0, len(providersInRequest)+1)
	for _, provider := range providersInRequest {
		client := ExtractClientCredentials(provider, headers)
		codexOAuth := codexAvailable && isCodexProvider(provider)
		if provider == providers.ProviderCodex {
			if codexOAuth {
				bindings = append(bindings, router.CredentialBinding{
					Provider:  provider,
					Source:    router.CredentialSourceCodexOAuth,
					Models:    codexCoveredModelSet(),
					Endpoints: map[router.TranslationEndpoint]struct{}{router.EndpointOpenAIResponses: {}},
				})
			}
			continue
		}

		// Codex OAuth is subscription-first for the legacy OpenAI provider. Keep
		// its binding scoped; any BYOK/deployment binding appended below can still
		// serve the provider's infrastructure models.
		if provider == providers.ProviderOpenAI && codexOAuth {
			bindings = append(bindings, router.CredentialBinding{
				Provider:  provider,
				Source:    router.CredentialSourceCodexOAuth,
				Models:    codexCoveredModelSet(),
				Endpoints: map[router.TranslationEndpoint]struct{}{router.EndpointOpenAIResponses: {}},
			})
		}

		if _, ok := byok[provider]; ok {
			bindings = append(bindings, router.CredentialBinding{
				Provider: provider,
				Source:   router.CredentialSourceBYOK,
			})
		}
		if client != nil {
			source := credentialSourceForCredentials(client)
			if source != router.CredentialSourceCodexOAuth {
				bindings = append(bindings, router.CredentialBinding{Provider: provider, Source: source})
			}
		}
		if s.usesDeploymentCredential(provider) {
			bindings = append(bindings, router.CredentialBinding{
				Provider: provider,
				Source:   router.CredentialSourceDeploymentKey,
			})
		}
	}
	return bindings
}

func codexCoveredModelSet() map[string]struct{} {
	models := make(map[string]struct{}, len(codexCoveredModels))
	for _, model := range codexCoveredModels {
		models[model] = struct{}{}
	}
	return models
}

// codexDecisionCredentialSource returns the source for a native Codex
// decision. Keeping this helper provider-specific prevents a future generic
// OAuth source from being mistaken for a Codex subscription.
func codexDecisionCredentialSource(decision router.Decision) router.Decision {
	if isCodexProvider(decision.Provider) && decision.Reason == codexOAuthPassthroughReason {
		decision.CredentialSource = router.CredentialSourceCodexOAuth
	}
	return decision
}

// attachDispatchPlan freezes the non-secret dispatch contract after credential
// resolution. Existing dispatch code still reads Decision.Model and Provider;
// the plan gives newer consumers one authoritative object during migration.
func (s *Service) attachDispatchPlan(ctx context.Context, req router.Request, decision router.Decision) router.Decision {
	if decision.Model == "" || decision.Provider == "" {
		return decision
	}
	requirements := req.TranslationRequirements
	if contextRequirements, ok := translationRequirementsFromContext(ctx); ok {
		requirements = contextRequirements
	}

	upstreamID := decision.Model
	if binding, ok := catalog.ResolveBindingWithCustom(
		decision.Model,
		map[string]struct{}{decision.Provider: {}},
		req.CustomBindings,
	); ok {
		upstreamID = catalog.UpstreamIDFor(decision.Model, binding.UpstreamID)
	}

	mode := router.DispatchModeTranslated
	if nativeDispatchFor(requirements, decision.Provider) {
		mode = router.DispatchModeNative
	}
	source := decision.CredentialSource
	if source == router.CredentialSourceUnknown {
		source = router.CredentialSourceFor(
			decision.Provider,
			decision.Model,
			requirements.Endpoint,
			req.CredentialBindings,
		)
	}
	decision.DispatchPlan = &router.DispatchPlan{
		Candidate: router.RoutingCandidate{
			Model:            decision.Model,
			Provider:         decision.Provider,
			UpstreamID:       upstreamID,
			CredentialSource: source,
			SourceFormat:     requirements.SourceFormat,
			Endpoint:         requirements.Endpoint,
			Mode:             mode,
			NativeOnly:       requirements.NativeOnly,
		},
		FallbackAllowed: s.shouldFailover(ctx),
	}
	return decision
}

func nativeDispatchFor(requirements router.TranslationRequirements, provider string) bool {
	switch requirements.SourceFormat {
	case router.WireFormatAnthropic:
		return providers.FamilyFor(provider) == providers.FamilyAnthropic
	case router.WireFormatOpenAI:
		return providers.FamilyFor(provider) == providers.FamilyOpenAICompat
	case router.WireFormatGemini:
		return providers.FamilyFor(provider) == providers.FamilyGemini
	default:
		return false
	}
}
