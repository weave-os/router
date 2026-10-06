package proxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/subscriptions"
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
	rotationStart := s.clockNow()
	rotationCtx, cancelRotation := context.WithTimeout(ctx, sameBindingRetryBudget)
	defer cancelRotation()
	if existing, _ := ctx.Value(subscriptionRotationBudgetKey{}).(context.Context); existing != nil {
		cancelRotation()
		rotationCtx = existing
	}
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
			return errors.As(err, &abort)
		},
		// A managed-subscription turn never fails over to a paid binding; a
		// transient error on it still gets the same-binding retries.
		Bound: func(dispatch.Attempt, error) bool { return managedBinding },
		// Nil unless transient_rate_limit is on for this request.
		RetryDelay: s.throttleRetryDelay(ctx),
	}
	transport.Attempt = func(attemptCtx context.Context, attempt dispatch.Attempt, client providers.Client) error {
		decision := in.initialDecision
		decision.Provider = attempt.Target.Provider
		guarded := dispatch.GuardTarget(client, attempt.Target)
		retryStart := rotationStart
		if usage, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage); usage != nil && usage.IntendedModel == "" {
			usage.IntendedModel = decision.Model
		}
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
			if current := CredentialsFromContext(attemptCtx); current != nil && current.OAuth && !s.includedOnlySubscriptionTransport(decision.Provider) {
				if subscriptionAttemptOnly(ctx) || paidFallbackForbidden(ctx) || !s.managedProviderFallbackAvailable(ctx, subscriptions.ProviderCodex) && decision.Provider == providers.ProviderOpenAI || !s.managedProviderFallbackAvailable(ctx, subscriptions.ProviderClaude) && decision.Provider == providers.ProviderAnthropic {
					return dispatchAbort{err: ErrSubscriptionPoolUnavailable}
				}
				attemptCtx = resolveAndInjectCredentials(withSuppressedClaudeSubscription(withSuppressedCodexSubscription(attemptCtx)), decision.Provider, decision.Model, http.Header{})
			}
			credentialCtx, lease, managedAttempt, leaseErr := s.leaseManagedSubscription(withSubscriptionRotationDeadline(attemptCtx, rotationCtx), decision.Provider, decision.Model)
			if leaseErr != nil {
				return dispatchAbort{err: leaseErr}
			}
			if !managedAttempt {
				credentialCtx = attemptCtx
			} else {
				// The lease ran under the rotation deadline; dispatch must not inherit it.
				credentialCtx = context.WithValue(attemptCtx, CredentialsContextKey{}, requestcontext.CredentialsFromContext(credentialCtx))
			}
			managedBinding = managedBinding || managedAttempt
			if usage, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage); usage != nil && managedAttempt {
				if usage.AttemptedAccounts == nil {
					usage.AttemptedAccounts = make(map[string]struct{})
				}
				usage.AttemptedAccounts[lease.AccountID+"\x00"+decision.Model] = struct{}{}
			}
			stopRotationDeadline := func() {}
			if creds := CredentialsFromContext(credentialCtx); creds != nil && creds.OAuth {
				credentialCtx, stopRotationDeadline = withUncommittedRotationDeadline(credentialCtx, rotationCtx, in.buf)
			}
			if creds := CredentialsFromContext(credentialCtx); paidFallbackForbidden(ctx) && (creds == nil || !creds.OAuth) {
				return dispatchAbort{err: ErrCreditsExhaustedSubscriptionUnavailable}
			}
			if creds := CredentialsFromContext(credentialCtx); creds != nil && creds.OAuth {
				if state, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage); state != nil {
					state.SubscriptionAttempted = true
				}
			}
			attemptErr := in.attempt(credentialCtx, decision, guarded)
			stopRotationDeadline()
			// The attempt context is canceled now; health bookkeeping must still persist.
			bookkeepingCtx := context.WithoutCancel(credentialCtx)
			if !committed(in.buf) {
				s.recordSubscriptionModelRejection(bookkeepingCtx, decision.Provider, decision.Model, attemptErr)
			}
			lease.Release()
			if attemptErr == nil {
				if usage, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage); usage != nil {
					usage.WinningCredentials = requestcontext.CredentialsFromContext(credentialCtx)
					usage.Finished = true
				}
				if managedAttempt {
					s.markManagedSubscriptionServed(ctx, credentialCtx, lease)
					s.recordManagedSubscriptionSuccess(bookkeepingCtx, decision.Provider, decision.Model, lease)
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
			// Budget expiry rotates only the attempt it canceled; terminal errors still stop.
			canceledByRotationBudget := ctx.Err() == nil && rotationCtx.Err() != nil && (errors.Is(attemptErr, context.Canceled) || errors.Is(attemptErr, context.DeadlineExceeded))
			rotate := managedAttempt && (s.recordManagedSubscriptionFailure(bookkeepingCtx, decision.Provider, decision.Model, lease, attemptErr) || providers.IsRetryable(attemptErr) || canceledByRotationBudget)
			if !managedAttempt && servedOnCodexSubscription(credentialCtx) && !committed(in.buf) {
				_, quotaSpent := codexQuotaExhaustion(attemptErr)
				if quotaSpent || codexOAuthCredentialRejected(attemptErr) || codexSubscriptionModelRejected(attemptErr) || providers.IsRetryable(attemptErr) || canceledByRotationBudget {
					s.recordCodexQuotaExhaustion(bookkeepingCtx, http.Header{}, attemptErr)
					attemptCtx = resolveAndInjectCredentials(withSuppressedCodexSubscription(attemptCtx), decision.Provider, decision.Model, http.Header{})
					rotate = true
				}
			}
			if committed(in.buf) || !rotate {
				if providers.IsUpstreamModelNotFound(attemptErr) {
					s.rememberGatewayLacksModel(attemptCtx, decision.Provider, decision.Model)
				}
				return attemptErr
			}
			if spent := s.clockNow().Sub(retryStart); spent >= sameBindingRetryBudget || rotationCtx.Err() != nil {
				log.Warn("dispatchWithFallback: subscription account rotation budget spent, not retrying",
					"model", decision.Model,
					"provider", decision.Provider,
					"spent_ms", spent.Milliseconds(),
					"budget_ms", sameBindingRetryBudget.Milliseconds(),
					"subscription_account_attempt", account+1,
					"err", attemptErr)
				poolProvider, subscriptionProvider := managedSubscriptionProviderFromUpstream(decision.Provider, decision.Model)
				if subscriptionProvider && !subscriptionAttemptOnly(ctx) && !paidFallbackForbidden(ctx) && s.managedProviderFallbackAvailable(ctx, poolProvider) {
					if in.buf != nil {
						in.buf.Discard()
					}
					apiCtx := resolveAndInjectCredentials(withSuppressedClaudeSubscription(withSuppressedCodexSubscription(ctx)), decision.Provider, decision.Model, http.Header{})
					apiErr := in.attempt(apiCtx, decision, guarded)
					if apiErr == nil {
						recordWinningCredentials(ctx, apiCtx)
					}
					return apiErr
				}
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
		// An abort (e.g. no subscription account to lease) renders on the same
		// terms as binding exhaustion. Skipping it left a released prelude with
		// no terminal frame, since the handler cannot write once the client has
		// bytes.
		if in.flushErr != nil && !in.deferFlushOnExhaustion && !committed(in.buf) {
			in.flushErr(in.w, abort.err)
		}
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

// WithInferenceDeployment records the boot-validated deployment facts so the
// inspection API can report how each purpose resolves here.
func (s *Service) WithInferenceDeployment(config policy.DeploymentPolicyConfig) *Service {
	s.inferenceDeployment = config
	return s
}

// InferencePolicyRegistry returns the registry every plan is resolved against.
func (s *Service) InferencePolicyRegistry() policy.Registry {
	return policy.DefaultRegistry()
}

// InferenceDeploymentProjection renders the registry against this deployment.
func (s *Service) InferenceDeploymentProjection() policy.DeploymentProjection {
	return policy.DefaultRegistry().DeploymentProjection(s.inferenceDeployment)
}

// InspectInferencePlan previews resolution for one purpose without dispatching.
func (s *Service) InspectInferencePlan(request policy.InspectionRequest) (policy.ResolvedPlan, error) {
	plans, err := s.inferencePlans()
	if err != nil {
		return policy.ResolvedPlan{}, err
	}
	return plans.Inspect(request, s.inferenceDeployment)
}

type subscriptionDeadlineContext struct {
	context.Context
	budget context.Context
}

func (c subscriptionDeadlineContext) Deadline() (time.Time, bool) { return c.budget.Deadline() }
func (c subscriptionDeadlineContext) Done() <-chan struct{}       { return c.budget.Done() }
func (c subscriptionDeadlineContext) Err() error                  { return c.budget.Err() }
func withSubscriptionRotationDeadline(ctx, budget context.Context) context.Context {
	return subscriptionDeadlineContext{Context: ctx, budget: budget}
}

// withUncommittedRotationDeadline cancels a subscription attempt when the
// rotation budget expires before provider output commits. The budget bounds
// rotation, not the length of a committed stream.
func withUncommittedRotationDeadline(ctx, budget context.Context, buf *preludeBuffer) (context.Context, func()) {
	attemptCtx, cancel := context.WithCancelCause(ctx)
	var attemptGeneration uint64
	if buf != nil {
		attemptGeneration = buf.currentAttemptGeneration()
	}
	stop := context.AfterFunc(budget, func() {
		if buf == nil {
			cancel(budget.Err())
			return
		}
		buf.abortIfUncommitted(attemptGeneration, func() { cancel(budget.Err()) })
	})
	return attemptCtx, func() {
		stop()
		cancel(nil)
	}
}

func recordWinningCredentials(ctx, credentialCtx context.Context) {
	if usage, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage); usage != nil {
		usage.WinningCredentials = requestcontext.CredentialsFromContext(credentialCtx)
		usage.Finished = true
	}
}
