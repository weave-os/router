package credentials

import (
	"context"
	"net/http"

	"weave-os/router/internal/auth"
)

type Broker interface {
	Eligibility(ctx context.Context, req EligibilityRequest) Eligibility
}

type EligibilityRequest struct {
	Registered                    map[string]struct{}
	SurfaceProvider               string
	Headers                       http.Header
	ExternalKeys                  []*auth.ExternalAPIKey
	RouterKeyAuthed               bool
	SubscriptionLanes             map[string]struct{}
	InstallationExcludedProviders []string
	SessionDisabledProviders      []string
	SubscriptionProviders         map[string]struct{}
	ExcludedModels                map[string]struct{}
}

type Eligibility struct {
	EnabledProviders       map[string]struct{}
	ExcludedProviders      map[string]struct{}
	GatewayProviders       map[string]struct{}
	SubscriptionProviders  map[string]struct{}
	CustomBindings         map[string][]string
	ExcludedModels         map[string]struct{}
	UsesNonDeploymentCreds bool
}
