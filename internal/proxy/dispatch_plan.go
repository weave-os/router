package proxy

import (
	"context"
	"errors"
	"strings"

	"weave-os/router/internal/credentials"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
)

func (s *Service) WithInferencePlans(plans *policy.PlanResolver) *Service {
	s.engine.SetPlans(plans)
	return s
}

func (s *Service) inferencePlans() (*policy.PlanResolver, error) {
	return s.engine.ResolvePlans()
}

func (s *Service) inferenceExecutor() (*dispatch.Executor, error) {
	return s.engine.ResolveExecutor(s.clock)
}

func (s *Service) WithInferenceDeployment(config policy.DeploymentPolicyConfig) *Service {
	s.engine.SetDeployment(config)
	return s
}

func (s *Service) InferencePolicyRegistry() policy.Registry {
	return policy.DefaultRegistry()
}

func (s *Service) InferenceDeploymentProjection() policy.DeploymentProjection {
	return policy.DefaultRegistry().DeploymentProjection(s.engine.Deployment())
}

func (s *Service) InspectInferencePlan(request policy.InspectionRequest) (policy.ResolvedPlan, error) {
	plans, err := s.inferencePlans()
	if err != nil {
		return policy.ResolvedPlan{}, err
	}
	return plans.Inspect(request, s.engine.Deployment())
}

func (s *Service) afterSubscriptionRotationBudget(ctx context.Context, decision router.Decision, subscriptionAttempts int, rotationErr error) error {
	observability.FromContext(ctx).Warn("Subscription admission budget spent; refusing unproven paid recovery", "model", decision.Model, "provider", decision.Provider, "budget_ms", subscriptionRotationBudget.Milliseconds(), "subscription_account_attempt", subscriptionAttempts, "err", rotationErr)
	if forcedClaudeClientAppRefused(ctx, decision, nil) {
		return errors.Join(subscriptionClientAppRefusal(ctx), rotationErr)
	}
	if errors.Is(rotationErr, ErrSubscriptionPoolUnavailable) {
		return rotationErr
	}
	return errors.Join(subscriptionPoolExhausted(ctx), rotationErr)
}

func recordWinningCredentials(ctx, credentialCtx context.Context) {
	if usage, _ := ctx.Value(credentials.ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage); usage != nil {
		usage.WinningCredentials = requestcontext.CredentialsFromContext(credentialCtx)
		usage.Finished = true
	}
}

func gatewayDeploymentBaseURL(provider string, client providers.Client) string {
	if !providers.IsGateway(provider) {
		return ""
	}
	for client != nil {
		if c, ok := client.(interface{ BaseURL() string }); ok {
			return strings.TrimRight(c.BaseURL(), "/")
		}
		w, ok := client.(interface{ Unwrap() providers.Client })
		if !ok {
			return ""
		}
		client = w.Unwrap()
	}
	return ""
}
