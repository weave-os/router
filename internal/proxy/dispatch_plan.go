package proxy

import (
	"strings"

	"context"
	"errors"
	"fmt"
	"net/http"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/inference"
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

type dispatchAbort struct{ err error }

func (e dispatchAbort) Error() string { return e.err.Error() }
func (e dispatchAbort) Unwrap() error { return e.err }

func (s *Service) dispatchPlanned(ctx context.Context, in dispatch.FailoverInputs, plan inference.ResolvedPlan) (winnerIdx int, err error) {
	executor, err := s.inferenceExecutor()
	if err != nil {
		return -1, err
	}

	lastIdx := 0
	lastProvider := in.InitialDecision.Provider
	ctx, accounts := subscriptionAccountPool{s: s}.Begin(ctx, in.InitialDecision)
	transport := dispatch.Transport{
		OperationID: string(in.Purpose),
		Committed:   func() bool { return committed(in.Prelude) },
		Reset: func() {
			if in.Prelude != nil {
				in.Prelude.Discard()
			}
		},
		Prepare: func(ctx context.Context, attempt dispatch.Attempt) (context.Context, error) {
			lastIdx = attempt.Index
			lastProvider = attempt.Target.Provider
			if attempt.Index == 0 {
				return ctx, nil
			}
			return resolveAndInjectCredentials(ctx, attempt.Target.Provider, in.InitialDecision.Model, http.Header{}), nil
		},
		Terminal: func(_ dispatch.Attempt, err error) bool {
			var abort dispatchAbort
			return errors.As(err, &abort)
		},
		Bound:      func(dispatch.Attempt, error) bool { return accounts.Bound() },
		RetryDelay: dispatch.ThrottleLedgerFromContext(ctx).RetryDelay(s.clock.Now),
	}
	transport.Attempt = func(attemptCtx context.Context, attempt dispatch.Attempt, client providers.Client) error {
		return accounts.Serve(ctx, attemptCtx, dispatch.AccountAttempt{Inputs: in, Plan: plan, Attempt: attempt, Client: client})
	}

	if trace := attemptTraceFromContext(ctx); trace != nil {
		transport.Describe = func(attempt dispatch.Attempt, err error) inference.AttemptDiagnosis {
			return describeAttempt(ctx, trace, attempt, err)
		}
	}
	req := inference.InvocationRequest{Purpose: in.Purpose, RequestID: observability.RequestIDFromContext(ctx)}
	result, runErr := executor.Run(ctx, req, plan, transport)
	s.recordOverloadOutcome(ctx, in.InitialDecision.Model, lastProvider, result.Outcome.ResponseCommitted, runErr)
	if runErr == nil {
		return lastIdx, nil
	}
	var abort dispatchAbort
	switch reason := result.Summary.FallbackReason; {
	case errors.As(runErr, &abort):
		runErr = attributeCodexUsageLimitAbort(attemptTraceFromContext(ctx), abort.err)
	case reason == dispatch.FailureReasonProviderNotConfigured, reason == dispatch.FailureReasonPrepare:
		return lastIdx, runErr
	default:
		runErr = attributeTerminalError(attemptTraceFromContext(ctx), runErr)
	}
	if in.FlushErr != nil && !in.DeferFlushOnExhaustion && !committed(in.Prelude) {
		in.FlushErr(in.Writer, runErr)
	}
	return lastIdx, runErr
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
	if usage, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage); usage != nil {
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
