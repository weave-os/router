// Package credentials declares the Credential Brokerage port: which providers,
// bindings and subscription lanes a request's credentials can reach.
package credentials

import (
	"context"
	"net/http"

	"github.com/google/uuid"
)

// Broker resolves the provider eligibility a request's credentials grant.
type Broker interface {
	Eligibility(ctx context.Context, req EligibilityRequest) (Eligibility, error)
}

// EligibilityRequest identifies the inbound request whose credentials are
// resolved.
type EligibilityRequest struct {
	// Surface is the wire-format provider: anthropic, openai or google.
	Surface string
	// Headers carries the inbound bearer used for OAuth enrollment.
	Headers        http.Header
	InstallationID uuid.UUID
}

// Eligibility is the provider reach of one request's credentials.
type Eligibility struct {
	// EnabledProviders is the narrowed provider set routing may choose from.
	EnabledProviders  map[string]struct{}
	ExcludedProviders map[string]struct{}
	// CustomBindings maps a model to the BYOK providers aliasing it.
	CustomBindings map[string][]string
	// GatewayProviders are the BYOK gateways that exclusively serve a tenant.
	GatewayProviders      map[string]struct{}
	SubscriptionProviders map[string]struct{}
	// ExcludedModels are models the credentials cannot reach at all.
	ExcludedModels map[string]struct{}
	// UsesNonDeploymentCreds reports a request served on tenant credentials.
	UsesNonDeploymentCreds bool
}
