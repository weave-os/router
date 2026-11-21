package proxy

import (
	"strings"

	"context"
	"errors"
	"fmt"

	"weave-os/router/internal/credentials"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
)

func (s *Service) WithInferencePlans(plans *policy.PlanResolver) *Service {
	s.plans = plans
	return s
}

func (s *Service) inferencePlans() (*policy.PlanResolver, error) {
	if s.plans != nil {
		return s.plans, nil
	}
	var err error
	s.defaultPlansOnce.Do(func() {
		var plans *policy.PlanResolver
		plans, err = policy.NewPlanResolver(policy.DefaultRegistry(), policy.NewResolver(
			nil, nil, func(model catalog.Model) string { return model.ID }, policy.ProviderPolicy{}))
		if err == nil {
			s.plans = plans
		}
	})
	if err != nil {
		return nil, fmt.Errorf("inference plan resolver: %w", err)
	}
	if s.plans == nil {
		return nil, errors.New("inference plan resolver unavailable")
	}
	return s.plans, nil
}

func (s *Service) inferenceExecutor() (*dispatch.Executor, error) {
	if s.executor != nil {
		return s.executor, nil
	}
	var err error
	s.defaultExecutorOnce.Do(func() {
		opts := []dispatch.Option{dispatch.WithClock(s.clock.Now)}
		if s.retrySleep != nil {
			opts = append(opts, dispatch.WithSleep(s.retrySleep))
		}
		s.executor, err = dispatch.NewExecutor(s.clients, opts...)
	})
	if s.executor == nil {
		if err == nil {
			err = errors.New("dispatch executor unavailable")
		}
		return nil, err
	}
	return s.executor, nil
}

func routedOrigin(decision router.Decision, hardPinned, stickyHit bool) policy.OverrideSource {
	return dispatch.RoutedOrigin(decision, hardPinned, stickyHit)
}

func (s *Service) WithInferenceDeployment(config policy.DeploymentPolicyConfig) *Service {
	s.inferenceDeployment = config
	return s
}

func (s *Service) InferencePolicyRegistry() policy.Registry {
	return policy.DefaultRegistry()
}

func (s *Service) InferenceDeploymentProjection() policy.DeploymentProjection {
	return policy.DefaultRegistry().DeploymentProjection(s.inferenceDeployment)
}

func (s *Service) InspectInferencePlan(request policy.InspectionRequest) (policy.ResolvedPlan, error) {
	plans, err := s.inferencePlans()
	if err != nil {
		return policy.ResolvedPlan{}, err
	}
	return plans.Inspect(request, s.inferenceDeployment)
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
