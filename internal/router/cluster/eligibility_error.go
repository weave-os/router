package cluster

import (
	"fmt"

	"workweave/router/internal/router"
)

// NoEligibleProviderError explains a provider eligibility failure without
// exposing credentials. It wraps ErrNoEligibleProvider for compatibility with
// existing callers and makes the safe exclusion details available to logs and
// diagnostics.
type NoEligibleProviderError struct {
	RequestedModel   string
	EnabledProviders []string
	Diagnostics      []router.CandidateDiagnostic
	Message          string
	Cause            error
}

// Error returns the stable routing failure message and its sentinel cause.
func (e *NoEligibleProviderError) Error() string {
	message := e.Message
	if message == "" {
		message = "no eligible provider for request"
	}
	if e.Cause == nil {
		return message
	}
	return fmt.Sprintf("%s: %v", message, e.Cause)
}

// Unwrap preserves errors.Is checks against ErrNoEligibleProvider.
func (e *NoEligibleProviderError) Unwrap() error {
	if e.Cause == nil {
		return ErrNoEligibleProvider
	}
	return e.Cause
}

func providerEligibilityDiagnostics(req router.Request, candidates []DeployedEntry) []router.CandidateDiagnostic {
	diagnostics := make([]router.CandidateDiagnostic, 0, len(candidates))
	for _, candidate := range candidates {
		if _, enabled := req.EnabledProviders[candidate.Provider]; !enabled {
			diagnostics = append(diagnostics, router.CandidateDiagnostic{
				Model:    candidate.Model,
				Provider: candidate.Provider,
				Endpoint: req.TranslationRequirements.Endpoint,
				Reason:   router.CandidateExclusionCredentialMissing,
			})
			continue
		}

		var providerMatched bool
		reason := router.CandidateExclusionCredentialScope
		var source router.CredentialSource
		for _, binding := range req.CredentialBindings {
			if binding.Provider != candidate.Provider {
				continue
			}
			providerMatched = true
			modelOK := binding.AllowsModel(candidate.Model)
			endpointOK := binding.AllowsEndpoint(req.TranslationRequirements.Endpoint)
			if modelOK && endpointOK {
				providerMatched = false
				break
			}
			if !modelOK && endpointOK {
				reason = router.CandidateExclusionModelUnsupported
			} else if modelOK && !endpointOK {
				reason = router.CandidateExclusionEndpointUnsupported
			}
			if source == router.CredentialSourceUnknown {
				source = binding.Source
			}
		}
		if !providerMatched {
			continue
		}
		diagnostics = append(diagnostics, router.CandidateDiagnostic{
			Model:            candidate.Model,
			Provider:         candidate.Provider,
			Endpoint:         req.TranslationRequirements.Endpoint,
			CredentialSource: source,
			Reason:           reason,
		})
	}
	return diagnostics
}
