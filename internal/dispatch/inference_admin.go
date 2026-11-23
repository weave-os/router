package dispatch

import (
	"context"
	"errors"
	"fmt"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router/policy"
)

var ErrModelListingUnsupported = errors.New("provider does not support model listing")

type InferencePlanInspector interface {
	InferencePolicyRegistry() policy.Registry
	InferenceDeploymentProjection() policy.DeploymentProjection
	InspectInferencePlan(request policy.InspectionRequest) (policy.ResolvedPlan, error)
}

type UpstreamModelLister interface {
	ListUpstreamModels(ctx context.Context, provider string, creds *requestcontext.Credentials) ([]string, error)
}

func (e *Engine) InferencePolicyRegistry() policy.Registry {
	return policy.DefaultRegistry()
}

func (e *Engine) InferenceDeploymentProjection() policy.DeploymentProjection {
	return policy.DefaultRegistry().DeploymentProjection(e.Deployment())
}

func (e *Engine) InspectInferencePlan(request policy.InspectionRequest) (policy.ResolvedPlan, error) {
	plans, err := e.ResolvePlans()
	if err != nil {
		return policy.ResolvedPlan{}, err
	}
	return plans.Inspect(request, e.Deployment())
}

func (e *Engine) ListUpstreamModels(ctx context.Context, provider string, creds *requestcontext.Credentials) ([]string, error) {
	client, err := e.Clients().Client(provider)
	if err != nil {
		return nil, err
	}
	lister, ok := client.(providers.ModelLister)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrModelListingUnsupported, provider)
	}
	if creds != nil {
		ctx = context.WithValue(ctx, requestcontext.CredentialsContextKey{}, creds)
	}
	return lister.ListModels(ctx)
}
