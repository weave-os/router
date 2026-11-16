package proxy

import (
	"context"
	"slices"
	"strconv"
	"time"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
	randomrouter "weave-os/router/internal/router/random_router"
)

func dispatchRandomPlanned(s *Service, ctx context.Context, in failoverInputs, plan inference.ResolvedPlan) (int, error) {
	state := randomrouter.RequestFrom(ctx)
	if state == nil || !state.Prepared || state.ProviderCalls != 0 || state.Lease.AccountID != state.Config.SubscriptionAccountID || !state.Config.ExpiresAt.After(s.clockNow()) {
		return -1, ErrSubscriptionPoolUnavailable
	}
	executor, err := s.inferenceExecutor()
	if err != nil {
		return -1, err
	}
	trace := attemptTraceFromContext(ctx)
	ctx = context.WithValue(ctx, CredentialsContextKey{}, state.Credentials)
	transport := dispatch.Transport{OperationID: string(in.purpose), Committed: func() bool { return committed(in.buf) }, Terminal: func(dispatch.Attempt, error) bool { return true }}
	transport.Attempt = func(callCtx context.Context, attempt dispatch.Attempt, client providers.Client) error {
		if state.ProviderCalls != 0 {
			return ErrSubscriptionPoolUnavailable
		}
		guarded := dispatch.GuardTarget(client, attempt.Target)
		if trace != nil {
			trace.BeginAttempt(attempt.Target, gatewayDeploymentBaseURL(attempt.Target.Provider, client), time.Now())
			guarded = dispatch.ObservePrepared(guarded, observeAttemptSent(trace))
		}
		in.w.Header().Set(HeaderRouterModel, in.initialDecision.Model)
		in.w.Header().Set(HeaderRouterProvider, in.initialDecision.Provider)
		in.w.Header().Set(HeaderRouterContextWindow, strconv.Itoa(contextWindowForRequest(in.initialDecision.Model, in.initialDecision.Provider)))
		state.ProviderCalls++
		err := in.attempt(callCtx, in.initialDecision, guarded)
		bookkeeping := context.WithoutCancel(callCtx)
		if err != nil {
			s.recordManagedSubscriptionFailure(bookkeeping, in.initialDecision.Provider, in.initialDecision.Model, state.Lease, err)
			return err
		}
		if usage, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage); usage != nil {
			usage.WinningCredentials = requestcontext.CredentialsFromContext(callCtx)
			usage.Finished = true
			usage.IntendedModel = in.initialDecision.Model
		}
		s.markManagedSubscriptionServed(ctx, callCtx, state.Lease)
		s.recordManagedSubscriptionSuccess(bookkeeping, in.initialDecision.Provider, in.initialDecision.Model, state.Lease)
		return nil
	}
	if trace != nil {
		transport.Describe = func(attempt dispatch.Attempt, err error) inference.AttemptDiagnosis {
			return describeAttempt(ctx, trace, attempt, err)
		}
	}
	_, err = executor.Run(ctx, inference.InvocationRequest{Purpose: in.purpose, RequestID: observability.RequestIDFromContext(ctx)}, plan, transport)
	if err != nil && in.flushErr != nil && !in.deferFlushOnExhaustion && !committed(in.buf) {
		in.flushErr(in.w, err)
	}
	return 0, err
}

func dispatchRandomRequest(s *Service, ctx context.Context, in failoverInputs) (int, error) {
	if randomBenchmarkFrom(ctx) == nil {
		return -1, randomrouter.ErrUnavailable
	}
	if in.purpose != inference.PurposeOpenAIResponses || in.initialDecision.Provider != providers.ProviderOpenAI || !slices.Contains(randomrouter.Models(), in.initialDecision.Model) {
		return -1, randomrouter.ErrUnavailable
	}
	ctx = s.withSubscriptionFundingAuthorization(ctx, in.subscriptionStateHeaders)
	plans, err := s.inferencePlans()
	if err != nil {
		return -1, err
	}
	in.bindings = []catalog.ProviderBinding{{Provider: providers.ProviderOpenAI}}
	plan, err := plans.ResolveRouted(policy.RoutedResolutionRequest{Purpose: policy.PurposeOpenAIResponses, Decision: in.initialDecision, Bindings: in.bindings, Budget: &policy.BudgetOverride{Source: policy.BudgetSourceRequest, MaxAttempts: 1}})
	if err != nil {
		return -1, err
	}
	return dispatchRandomPlanned(s, ctx, in, plan)
}

func subscriptionStateDispatchEnabled(ctx context.Context, in failoverInputs) bool {
	return in.subscriptionStateRequest != nil && subscriptionStateModelsEnabled(ctx) && in.buildAlternative != nil
}
