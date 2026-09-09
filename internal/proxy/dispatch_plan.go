package proxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/translate"
)

// WithInferencePlans installs the policy resolver that authorizes routed
// main-inference decisions before the executor dispatches them.
func (s *Service) WithInferencePlans(plans *policy.PlanResolver) *Service {
	s.plans = plans
	return s
}

// inferencePlans returns the wired plan resolver, or a registry-only default
// so a Service built without the composition root still resolves routed
// decisions against the reviewed registry.
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

// inferenceExecutor returns the wired executor, or one built over the
// service's own client registry and injectable clock so tests without a
// composition root still dispatch through the boundary.
func (s *Service) inferenceExecutor() (*dispatch.Executor, error) {
	if s.executor != nil {
		return s.executor, nil
	}
	var err error
	s.defaultExecutorOnce.Do(func() {
		opts := []dispatch.Option{dispatch.WithClock(s.clockNow)}
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

// routedOrigin names the override source that fixed a routed decision's
// model; empty when the router itself selected it.
func routedOrigin(decision router.Decision, hardPinned, stickyHit bool) policy.OverrideSource {
	switch {
	case decision.Reason == translate.ReasonUserForceModel:
		return policy.OverrideSourceRequest
	case hardPinned:
		return policy.OverrideSourceDeployment
	case stickyHit:
		return policy.OverrideSourceSession
	}
	return ""
}

// dispatchAbort marks an attempt failure the surface must return as-is: no
// failover, no error envelope (a credential lease could not be obtained).
type dispatchAbort struct{ err error }

func (e dispatchAbort) Error() string { return e.err.Error() }
func (e dispatchAbort) Unwrap() error { return e.err }

// dispatchPlanned runs the surface's attempt closure through the dispatch
// executor for a resolved plan, keeping the surface-level failover contract:
// per-binding headers, credential re-resolution on fallback, managed
// subscription leasing and account rotation, prelude discard on retry, and
// the entry point's own error rendering on exhaustion.
func (s *Service) dispatchPlanned(ctx context.Context, in failoverInputs, plan inference.ResolvedPlan) (winnerIdx int, err error) {
	log := observability.FromContext(ctx)
	executor, err := s.inferenceExecutor()
	if err != nil {
		return -1, err
	}

	lastIdx := 0
	managedBinding := false
	transport := dispatch.Transport{
		OperationID: string(in.purpose),
		Committed:   func() bool { return committed(in.buf) },
		Reset: func() {
			if in.buf != nil {
				in.buf.Discard()
			}
		},
		Prepare: func(ctx context.Context, attempt dispatch.Attempt) (context.Context, error) {
			lastIdx = attempt.Index
			if attempt.Index == 0 {
				return ctx, nil
			}
			// Fallback attempts re-resolve credentials against an empty header
			// set: shouldFailover() already ruled out BYOK/client-credential
			// paths, so the only source left is the deployment env key.
			return resolveAndInjectCredentials(ctx, attempt.Target.Provider, in.initialDecision.Model, http.Header{}), nil
		},
		Terminal: func(_ dispatch.Attempt, err error) bool {
			var abort dispatchAbort
			return managedBinding || errors.As(err, &abort)
		},
	}
	transport.Attempt = func(attemptCtx context.Context, attempt dispatch.Attempt, client providers.Client) error {
		decision := in.initialDecision
		decision.Provider = attempt.Target.Provider
		guarded := dispatch.GuardTarget(client, attempt.Target)
		retryStart := s.clockNow()
		for account := 0; ; account++ {
			if !committed(in.buf) {
				in.w.Header().Set(HeaderRouterProvider, decision.Provider)
				in.w.Header().Set(HeaderRouterModel, decision.Model)
				in.w.Header().Set(HeaderRouterContextWindow, strconv.Itoa(contextWindowForRequest(decision.Model, decision.Provider)))
				if attempt.Index > 0 {
					in.w.Header().Set(HeaderRouterFallbackFrom, in.bindings[0].Provider)
					in.w.Header().Set(HeaderRouterFallbackAttempt, attemptIdxLabel(attempt.Index))
				}
			}
			credentialCtx, lease, managedAttempt, leaseErr := s.leaseManagedSubscription(attemptCtx, decision.Provider, decision.Model)
			if leaseErr != nil {
				return dispatchAbort{err: leaseErr}
			}
			managedBinding = managedBinding || managedAttempt
			attemptErr := in.attempt(credentialCtx, decision, guarded)
			lease.Release()
			if attemptErr == nil {
				if managedAttempt {
					markManagedSubscriptionServed(ctx, credentialCtx)
				}
				if attempt.Index > 0 {
					log.Info("dispatchWithFallback: succeeded on fallback",
						"model", decision.Model,
						"primary_provider", in.bindings[0].Provider,
						"final_provider", decision.Provider,
						"attempt_index", attempt.Index)
				}
				return nil
			}
			rotate := managedAttempt && s.recordManagedSubscriptionFailure(credentialCtx, decision.Provider, decision.Model, lease, attemptErr)
			if committed(in.buf) || !rotate {
				if providers.IsUpstreamModelNotFound(attemptErr) {
					s.rememberGatewayLacksModel(attemptCtx, decision.Provider, decision.Model)
				}
				return attemptErr
			}
			if spent := s.clockNow().Sub(retryStart); spent >= sameBindingRetryBudget {
				log.Warn("dispatchWithFallback: subscription account rotation budget spent, not retrying",
					"model", decision.Model,
					"provider", decision.Provider,
					"spent_ms", spent.Milliseconds(),
					"budget_ms", sameBindingRetryBudget.Milliseconds(),
					"subscription_account_attempt", account+1,
					"err", attemptErr)
				return attemptErr
			}
			if in.buf != nil {
				in.buf.Discard()
			}
			log.Warn("dispatchWithFallback: retrying with another subscription account",
				"model", decision.Model,
				"provider", decision.Provider,
				"account_id", lease.AccountID,
				"same_binding_retry", account+1,
				"err", attemptErr)
		}
	}

	req := inference.InvocationRequest{Purpose: in.purpose, RequestID: observability.RequestIDFromContext(ctx)}
	result, runErr := executor.Run(ctx, req, plan, transport)
	if runErr == nil {
		return lastIdx, nil
	}
	var abort dispatchAbort
	if errors.As(runErr, &abort) {
		return lastIdx, abort.err
	}
	switch result.Summary.FallbackReason {
	case dispatch.FailureReasonProviderNotConfigured, dispatch.FailureReasonPrepare:
		return lastIdx, runErr
	}
	if in.flushErr != nil && !in.deferFlushOnExhaustion && !committed(in.buf) {
		in.flushErr(in.w, runErr)
	}
	return lastIdx, runErr
}
