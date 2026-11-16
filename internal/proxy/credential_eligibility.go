package proxy

import (
	"context"
	"net/http"

	"weave-os/router/internal/credentials"
	"weave-os/router/internal/providers"
)

func requestEligibility(ctx context.Context, s *Service, surface string, headers http.Header) (credentials.Eligibility, error) {
	enabled := s.enabledProvidersForRequest(ctx, surface, headers)
	if surface != providers.ProviderGoogle && paidCapacityUnfunded(ctx) {
		enabled = restrictToCustomerFundedProviders(ctx, headers, enabled)
	}
	enabled, err := applyWeaveCreditsPolicy(ctx, enabled)
	if err != nil {
		return credentials.Eligibility{}, err
	}
	return credentials.Eligibility{
		EnabledProviders: enabled,
		CustomBindings:   s.customBindingsForRequest(ctx),
		GatewayProviders: s.gatewayProvidersForRequest(ctx),
	}, nil
}
