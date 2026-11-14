package proxy

import (
	"context"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/providers"
)

type serviceCredentialBroker struct{ s *Service }

// NewCredentialBroker adapts the Service's credential resolution to the
// credentials.Broker port until Credential Brokerage owns it.
func NewCredentialBroker(s *Service) credentials.Broker {
	return serviceCredentialBroker{s: s}
}

func (b serviceCredentialBroker) Eligibility(ctx context.Context, req credentials.EligibilityRequest) (credentials.Eligibility, error) {
	enabled := b.s.enabledProvidersForRequest(ctx, req.Surface, req.Headers)
	// Linked-first is a funding preference; only depleted capacity restricts
	// selection to providers the caller's subscription can serve.
	if req.Surface != providers.ProviderGoogle && billing.SubscriptionOnlyFromContext(ctx) && !linkedFirst(ctx) {
		enabled = restrictToSubscriptionProviders(ctx, req.Headers, enabled)
	}
	enabled, err := applyWeaveCreditsPolicy(ctx, enabled)
	if err != nil {
		return credentials.Eligibility{}, err
	}
	return credentials.Eligibility{
		EnabledProviders: enabled,
		CustomBindings:   b.s.customBindingsForRequest(ctx),
		GatewayProviders: b.s.gatewayProvidersForRequest(ctx),
	}, nil
}
