// Package credentials is the Credential Brokerage bounded context: which
// upstream providers a request holds credentials for, and which credential
// kinds (deployment key, BYOK, client-supplied, subscription) it may spend.
package credentials

import (
	"context"
	"net/http"

	"weave-os/router/internal/auth"
)

// Broker resolves a request's provider eligibility from its credentials.
type Broker interface {
	Eligibility(ctx context.Context, req EligibilityRequest) Eligibility
}

// EligibilityRequest carries the request-scoped credential facts the
// orchestrator has already authenticated.
type EligibilityRequest struct {
	// Registered names every provider with a client; it stands in for
	// deployment keys when Brokerage.KeyedProviders is nil.
	Registered map[string]struct{}
	// SurfaceProvider is the inbound wire format's natural provider. A
	// client-supplied credential only ever enables this provider.
	SurfaceProvider string
	Headers         http.Header
	ExternalKeys    []*auth.ExternalAPIKey
	RouterKeyAuthed bool
	// SubscriptionLanes are providers a presented or linked subscription
	// enrolls regardless of the inbound bearer.
	SubscriptionLanes             map[string]struct{}
	InstallationExcludedProviders []string
	SessionDisabledProviders      []string
	SubscriptionProviders         map[string]struct{}
	ExcludedModels                map[string]struct{}
}

// Eligibility is the credential-derived input to routing.
type Eligibility struct {
	EnabledProviders  map[string]struct{}
	ExcludedProviders map[string]struct{}
	// GatewayProviders, when non-empty, displace every other upstream.
	GatewayProviders      map[string]struct{}
	SubscriptionProviders map[string]struct{}
	CustomBindings        map[string][]string
	ExcludedModels        map[string]struct{}
	// UsesNonDeploymentCreds keeps deployment-credentialed side calls (the
	// handover summarizer) off requests served on someone else's credential.
	UsesNonDeploymentCreds bool
}
