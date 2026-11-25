package proxy

import (
	"context"

	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/subscriptions/entitlement"
)

func internalBetaSession(ctx context.Context) bool {
	identity, managed := requestcontext.ServingIdentityFromContext(ctx)
	return managed && policyregistry.ExperimentalRoutingModelsPermitted(policyregistry.ServingTarget(identity.Target), entitlement.Plan(identity.Plan)) && identity.ReleaseID != "" && identity.BindingID != "" && identity.CredentialIdentity != ""
}
