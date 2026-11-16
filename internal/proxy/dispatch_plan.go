package proxy

import (
	"strings"
	"time"

	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/subscriptionlane"
	"weave-os/router/internal/subscriptions"
	"weave-os/router/internal/translate"
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

type dispatchAbort struct{ err error }

func (e dispatchAbort) Error() string { return e.err.Error() }
func (e dispatchAbort) Unwrap() error { return e.err }

func (s *Service) dispatchPlanned(ctx context.Context, in failoverInputs, plan inference.ResolvedPlan) (winnerIdx int, err error) {
	log := observability.FromContext(ctx)
	executor, err := s.inferenceExecutor()
	if err != nil {
		return -1, err
	}

	lastIdx := 0
	lastProvider := in.initialDecision.Provider
	managedBinding := false
	ctx, rotationBudget := withSubscriptionAdmissionBudget(ctx)
	ctx = withClientAppRefusalPaidFallback(ctx, in.initialDecision)
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
			lastProvider = attempt.Target.Provider
			if attempt.Index == 0 {
				return ctx, nil
			}
			return resolveAndInjectCredentials(ctx, attempt.Target.Provider, in.initialDecision.Model, http.Header{}), nil
		},
		Terminal: func(_ dispatch.Attempt, err error) bool {
			var abort dispatchAbort
			return errors.As(err, &abort)
		},
		Bound:      func(dispatch.Attempt, error) bool { return managedBinding },
		RetryDelay: dispatch.ThrottleLedgerFromContext(ctx).RetryDelay(s.clockNow),
	}
	transport.Attempt = func(attemptCtx context.Context, attempt dispatch.Attempt, client providers.Client) error {
		decision := in.initialDecision
		decision.Provider = attempt.Target.Provider
		guarded := dispatch.GuardTarget(client, attempt.Target)
		trace := attemptTraceFromContext(ctx)
		if trace != nil {
			trace.BeginAttempt(attempt.Target, gatewayDeploymentBaseURL(attempt.Target.Provider, client), time.Now())
			guarded = dispatch.ObservePrepared(guarded, observeAttemptSent(trace))
		}
		var lastSubscriptionErr error
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
			if current := CredentialsFromContext(attemptCtx); current != nil && current.OAuth && !s.supportsSubscriptionTransport(decision.Provider) {
				if subscriptionAttemptOnly(ctx) || paidFallbackForbidden(ctx) || !s.managedProviderFallbackAvailable(ctx, subscriptions.ProviderCodex) && decision.Provider == providers.ProviderOpenAI || !s.managedProviderFallbackAvailable(ctx, subscriptions.ProviderClaude) && decision.Provider == providers.ProviderAnthropic {
					return dispatchAbort{err: subscriptionPoolRefusal(ctx, subscriptionlane.SubscriptionPoolUnavailable)}
				}
				attemptCtx = resolveAndInjectCredentials(withSuppressedClaudeSubscription(withSuppressedCodexSubscription(attemptCtx)), decision.Provider, decision.Model, http.Header{})
			}
			if !subscriptionAPIOnly(attemptCtx) && rotationBudget.remaining <= 0 {
				return dispatchAbort{err: s.afterSubscriptionRotationBudget(ctx, decision, account, subscriptionPoolRefusal(ctx, subscriptionlane.SubscriptionPoolUnavailable))}
			}
			leaseCtx, finishLease := rotationBudget.leaseContext(attemptCtx, s.clockNow)
			if subscriptionAPIOnly(attemptCtx) {
				finishLease()
				leaseCtx = attemptCtx
				finishLease = func() {}
			}
			credentialCtx, lease, managedAttempt, leaseErr := s.leaseManagedSubscription(leaseCtx, decision.Provider, decision.Model)
			finishLease()
			if ctx.Err() != nil {
				lease.Release()
				return ctx.Err()
			}
			if leaseErr != nil {
				if forcedClaudeClientAppRefused(ctx, decision, nil) {
					return dispatchAbort{err: errors.Join(ErrSubscriptionClientAppRefused, leaseErr, lastSubscriptionErr)}
				}
				return dispatchAbort{err: errors.Join(leaseErr, lastSubscriptionErr)}
			}
			if !managedAttempt {
				credentialCtx = attemptCtx
			} else {
				credentialCtx = context.WithValue(attemptCtx, CredentialsContextKey{}, requestcontext.CredentialsFromContext(credentialCtx))
			}
			managedBinding = managedBinding || managedAttempt
			credentialCtx = s.withClaudeSubscriptionProfile(credentialCtx, nil)
			credentialCtx = withClaudeEnterpriseTurn(credentialCtx, decision)
			if usage, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage); usage != nil && managedAttempt {
				if usage.AttemptedAccounts == nil {
					usage.AttemptedAccounts = make(map[string]struct{})
				}
				usage.AttemptedAccounts[lease.AccountID+"\x00"+decision.Model] = struct{}{}
			}
			if forcedClaudeClientAppRefused(ctx, decision, CredentialsFromContext(credentialCtx)) {
				lease.Release()
				return dispatchAbort{err: ErrSubscriptionClientAppRefused}
			}
			if paidAttemptForbidden(ctx, CredentialsFromContext(credentialCtx)) {
				lease.Release()
				if claudeEnterpriseTurn(ctx) {
					return dispatchAbort{err: ErrClaudeEnterpriseSubscriptionUnavailable}
				}
				return dispatchAbort{err: subscriptionOnlyUnavailable(ctx)}
			}
			if creds := CredentialsFromContext(credentialCtx); creds != nil && creds.OAuth {
				if state, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage); state != nil {
					state.SubscriptionAttempted = true
					if creds.Source == credSourceSubscription && creds.SubscriptionPlan != "" {
						state.ClaudeSubscriptionPlan = creds.SubscriptionPlan
						state.ClaudePlanUnverified = state.ClaudePlanUnverified || creds.SubscriptionPlan == providers.SubscriptionPlanUnknown
					}
				}
			}
			attemptStarted := time.Now()
			attemptErr := in.attempt(credentialCtx, decision, guarded)

			bookkeepingCtx := context.WithoutCancel(credentialCtx)
			if managedAttempt {
				observability.FromContext(bookkeepingCtx).Info("Subscription account attempt completed",
					"subscription_event", auth.SubscriptionCredentialEventAttempted, "account_id", lease.AccountID,
					"provider", decision.Provider, "succeeded", attemptErr == nil,
					"upstream_status", upstreamStatus(attemptErr), "attempt_ms", time.Since(attemptStarted).Milliseconds(),
					"response_committed", committed(in.buf), "subscription_account_attempt", account+1)
			}
			if creds := CredentialsFromContext(credentialCtx); !managedAttempt && creds != nil && creds.OAuth && attemptErr != nil {
				s.recordDirectSubscriptionQuota(bookkeepingCtx, decision.Provider, decision.Model, creds, attemptErr)
			}
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
			if ctx.Err() != nil {
				return attemptErr
			}
			rotate := managedAttempt && (s.recordManagedSubscriptionFailure(bookkeepingCtx, decision.Provider, decision.Model, lease, attemptErr) || providers.IsRetryable(attemptErr))
			if managedAttempt && !committed(in.buf) {
				s.recordSubscriptionUnavailable(bookkeepingCtx, decision.Provider, decision.Model, attemptErr)
			}
			if !managedAttempt && !committed(in.buf) && codexSubscriptionRotates(credentialCtx, attemptErr) {
				s.recordCodexCredentialUnusable(bookkeepingCtx, CredentialsFromContext(credentialCtx), attemptErr)
				s.recordSubscriptionUnavailable(bookkeepingCtx, decision.Provider, decision.Model, attemptErr)
				attemptCtx = resolveAndInjectCredentials(withSuppressedCodexSubscription(attemptCtx), decision.Provider, decision.Model, http.Header{})
				rotate = true
			}
			if next, capped := claudeEnterpriseCapRotation(credentialCtx, attemptCtx, decision, attemptErr); capped && !managedAttempt && !committed(in.buf) {
				attemptCtx, rotate = next, true
			}
			if credentialRejectionStands(lastSubscriptionErr, attemptErr, codexOAuthCredentialRejected) {
				return lastSubscriptionErr
			}
			if committed(in.buf) || !rotate {
				if providers.IsUpstreamModelNotFound(attemptErr) {
					s.rememberGatewayLacksModel(attemptCtx, decision.Provider, decision.Model)
				}
				return attemptErr
			}
			if trace != nil {
				describeAttempt(ctx, trace, attempt, attemptErr)
				trace.BeginAttempt(attempt.Target, gatewayDeploymentBaseURL(attempt.Target.Provider, client), time.Now())
			}
			lastSubscriptionErr = attemptErr
			if rotationBudget.remaining <= 0 {
				return s.afterSubscriptionRotationBudget(ctx, decision, account+1, attemptErr)
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

	if trace := attemptTraceFromContext(ctx); trace != nil {
		transport.Describe = func(attempt dispatch.Attempt, err error) inference.AttemptDiagnosis {
			return describeAttempt(ctx, trace, attempt, err)
		}
	}
	req := inference.InvocationRequest{Purpose: in.purpose, RequestID: observability.RequestIDFromContext(ctx)}
	result, runErr := executor.Run(ctx, req, plan, transport)
	s.recordOverloadOutcome(ctx, in.initialDecision.Model, lastProvider, result.Outcome.ResponseCommitted, runErr)
	if runErr == nil {
		return lastIdx, nil
	}
	var abort dispatchAbort
	if errors.As(runErr, &abort) {
		if in.flushErr != nil && !in.deferFlushOnExhaustion && !committed(in.buf) {
			in.flushErr(in.w, abort.err)
		}
		return lastIdx, abort.err
	}
	switch result.Summary.FallbackReason {
	case dispatch.FailureReasonProviderNotConfigured, dispatch.FailureReasonPrepare:
		return lastIdx, runErr
	}
	runErr = attributeTerminalError(attemptTraceFromContext(ctx), runErr)
	if in.flushErr != nil && !in.deferFlushOnExhaustion && !committed(in.buf) {
		in.flushErr(in.w, runErr)
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
		return errors.Join(ErrSubscriptionClientAppRefused, rotationErr)
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
