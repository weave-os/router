package proxy

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/subscriptionlane"
	"weave-os/router/internal/subscriptions"
)

type subscriptionAccountPool struct {
	s *Service
}

var _ dispatch.SubscriptionAccountPool = subscriptionAccountPool{}

func (p subscriptionAccountPool) Begin(ctx context.Context, initial router.Decision) (context.Context, dispatch.SubscriptionAccounts) {
	ctx, budget := withSubscriptionAdmissionBudget(ctx)
	ctx = withClientAppRefusalPaidFallback(ctx, initial)
	return ctx, &subscriptionAccounts{s: p.s, rotationBudget: budget}
}

type subscriptionAccounts struct {
	s              *Service
	rotationBudget *subscriptionAdmissionBudget
	managedBinding bool
}

func (a *subscriptionAccounts) Bound() bool { return a.managedBinding }

type accountLoop struct {
	*subscriptionAccounts
	in                  dispatch.FailoverInputs
	plan                inference.ResolvedPlan
	attempt             dispatch.Attempt
	client              providers.Client
	guarded             providers.Client
	decision            router.Decision
	trace               *dispatch.AttemptTrace
	lastSubscriptionErr error
}

type leasedAccount struct {
	lease   subscriptions.Lease
	managed bool
	account int
}

func managedSubscriptionUsageOf(ctx context.Context) *credentials.ManagedSubscriptionUsage {
	usage, _ := ctx.Value(credentials.ManagedSubscriptionUsageContextKey{}).(*credentials.ManagedSubscriptionUsage)
	return usage
}

func (a *subscriptionAccounts) Serve(ctx, attemptCtx context.Context, attempt dispatch.AccountAttempt) error {
	l := &accountLoop{subscriptionAccounts: a, in: attempt.Inputs, plan: attempt.Plan, attempt: attempt.Attempt, client: attempt.Client}
	l.decision = attempt.Inputs.InitialDecision
	l.decision.Provider = attempt.Attempt.Target.Provider
	l.guarded = dispatch.GuardTarget(attempt.Client, attempt.Attempt.Target)
	l.trace = attemptTraceFromContext(ctx)
	if l.trace != nil {
		l.trace.BeginAttempt(attempt.Attempt.Target, gatewayDeploymentBaseURL(attempt.Attempt.Target.Provider, attempt.Client), a.s.clock.Now())
		l.guarded = dispatch.ObservePrepared(l.guarded, observeAttemptSent(l.trace))
	}
	if usage := managedSubscriptionUsageOf(ctx); usage != nil && usage.IntendedModel == "" {
		usage.IntendedModel = l.decision.Model
	}
	for account := 0; ; account++ {
		next, done, err := l.serveAccount(ctx, attemptCtx, account)
		if done {
			return err
		}
		attemptCtx = next
	}
}

func (l *accountLoop) serveAccount(ctx, attemptCtx context.Context, account int) (context.Context, bool, error) {
	if !committed(l.in.Prelude) {
		l.writeAttemptHeaders()
	}
	attemptCtx, err := l.oauthTransportFallback(ctx, attemptCtx)
	if err != nil {
		return nil, true, err
	}
	credentialCtx, leased, err := l.leaseAccount(ctx, attemptCtx, account)
	if err != nil {
		return nil, true, err
	}
	if err := l.admitLeased(ctx, credentialCtx, leased); err != nil {
		return nil, true, err
	}
	attemptStarted, attemptErr := invokeLeasedProviderAttempt(l.s, credentialCtx, l.plan, leasedProviderAttempt{inputs: l.in, decision: l.decision, target: l.attempt.Target, lease: leased.lease, client: l.guarded, managed: leased.managed})

	bookkeepingCtx := context.WithoutCancel(credentialCtx)
	l.recordAccountAttempt(bookkeepingCtx, credentialCtx, leased, attemptStarted, attemptErr)
	leased.lease.Release()
	if attemptErr == nil {
		l.recordServed(ctx, credentialCtx, bookkeepingCtx, leased)
		return nil, true, nil
	}
	if ctx.Err() != nil {
		return nil, true, attemptErr
	}
	attemptCtx, rotate := l.rotationAfterFailure(bookkeepingCtx, credentialCtx, attemptCtx, leased, attemptErr)
	if credentialRejectionStands(l.lastSubscriptionErr, attemptErr, codexOAuthCredentialRejected) {
		return nil, true, l.lastSubscriptionErr
	}
	if committed(l.in.Prelude) || !rotate {
		if providers.IsUpstreamModelNotFound(attemptErr) {
			l.s.rememberGatewayLacksModel(attemptCtx, l.decision.Provider, l.decision.Model)
		}
		return nil, true, attemptErr
	}
	if err := l.prepareNextAccount(ctx, leased, attemptErr); err != nil {
		return nil, true, err
	}
	return attemptCtx, false, nil
}

func (l *accountLoop) writeAttemptHeaders() {
	decision, header := l.decision, l.in.Writer.Header()
	header.Set(requestcontext.HeaderRouterProvider, decision.Provider)
	header.Set(requestcontext.HeaderRouterModel, decision.Model)
	header.Set(requestcontext.HeaderRouterContextWindow, strconv.Itoa(contextWindowForRequest(decision.Model, decision.Provider)))
	if l.attempt.Index > 0 {
		header.Set(requestcontext.HeaderRouterFallbackFrom, l.in.Bindings[0].Provider)
		header.Set(requestcontext.HeaderRouterFallbackAttempt, attemptIdxLabel(l.attempt.Index))
	}
}

func (l *accountLoop) oauthTransportFallback(ctx, attemptCtx context.Context) (context.Context, error) {
	s, decision := l.s, l.decision
	if current := CredentialsFromContext(attemptCtx); current != nil && current.OAuth && !s.supportsSubscriptionTransport(decision.Provider) {
		if subscriptionAttemptOnly(ctx) || paidFallbackForbidden(ctx) || !s.managedProviderFallbackAvailable(ctx, subscriptions.ProviderCodex) && decision.Provider == providers.ProviderOpenAI || !s.managedProviderFallbackAvailable(ctx, subscriptions.ProviderClaude) && decision.Provider == providers.ProviderAnthropic {
			return nil, dispatch.Abort{Err: subscriptionPoolRefusal(ctx, subscriptionlane.SubscriptionPoolUnavailable)}
		}
		attemptCtx = resolveAndInjectCredentials(credentials.WithSuppressedClaudeSubscription(withSuppressedCodexSubscription(attemptCtx)), decision.Provider, decision.Model, http.Header{})
	}
	return attemptCtx, nil
}

func (l *accountLoop) leaseAccount(ctx, attemptCtx context.Context, account int) (context.Context, leasedAccount, error) {
	s, decision, rotationBudget := l.s, l.decision, l.rotationBudget
	if !subscriptionAPIOnly(attemptCtx) && rotationBudget.remaining <= 0 {
		return nil, leasedAccount{}, dispatch.Abort{Err: s.afterSubscriptionRotationBudget(ctx, decision, account, subscriptionPoolRefusal(ctx, subscriptionlane.SubscriptionPoolUnavailable))}
	}
	leaseCtx, finishLease := rotationBudget.leaseContext(attemptCtx, s.clock.Now)
	if subscriptionAPIOnly(attemptCtx) {
		finishLease()
		leaseCtx = attemptCtx
		finishLease = func() {}
	}
	credentialCtx, lease, managedAttempt, leaseErr := s.leaseManagedSubscription(leaseCtx, decision.Provider, decision.Model)
	finishLease()
	if ctx.Err() != nil {
		lease.Release()
		return nil, leasedAccount{}, ctx.Err()
	}
	if leaseErr != nil {
		if forcedClaudeClientAppRefused(ctx, decision, nil) {
			return nil, leasedAccount{}, dispatch.Abort{Err: errors.Join(subscriptionClientAppRefusal(ctx), leaseErr, l.lastSubscriptionErr)}
		}
		return nil, leasedAccount{}, dispatch.Abort{Err: errors.Join(leaseErr, l.lastSubscriptionErr)}
	}
	if !managedAttempt {
		credentialCtx = attemptCtx
	} else {
		credentialCtx = context.WithValue(attemptCtx, CredentialsContextKey{}, requestcontext.CredentialsFromContext(credentialCtx))
	}
	l.managedBinding = l.managedBinding || managedAttempt
	credentialCtx = s.withClaudeSubscriptionProfile(credentialCtx, nil)
	credentialCtx = withClaudeEnterpriseTurn(credentialCtx, decision)
	return credentialCtx, leasedAccount{lease: lease, managed: managedAttempt, account: account}, nil
}

func (l *accountLoop) admitLeased(ctx, credentialCtx context.Context, leased leasedAccount) error {
	if usage := managedSubscriptionUsageOf(ctx); usage != nil && leased.managed {
		if usage.AttemptedAccounts == nil {
			usage.AttemptedAccounts = make(map[string]struct{})
		}
		usage.AttemptedAccounts[leased.lease.AccountID+"\x00"+l.decision.Model] = struct{}{}
	}
	if forcedClaudeClientAppRefused(ctx, l.decision, CredentialsFromContext(credentialCtx)) {
		leased.lease.Release()
		return dispatch.Abort{Err: subscriptionClientAppRefusal(ctx)}
	}
	if paidAttemptForbidden(ctx, CredentialsFromContext(credentialCtx)) {
		leased.lease.Release()
		if claudeEnterpriseTurn(ctx) {
			return dispatch.Abort{Err: claudeEnterpriseRefusal(ctx)}
		}
		return dispatch.Abort{Err: credentials.SubscriptionOnlyUnavailable(ctx)}
	}
	if creds := CredentialsFromContext(credentialCtx); creds != nil && creds.OAuth {
		if state := managedSubscriptionUsageOf(ctx); state != nil {
			state.SubscriptionAttempted = true
			if creds.Source == credSourceSubscription && creds.SubscriptionPlan != "" {
				state.ClaudeSubscriptionPlan = creds.SubscriptionPlan
				state.ClaudePlanUnverified = state.ClaudePlanUnverified || creds.SubscriptionPlan == providers.SubscriptionPlanUnknown
			}
		}
	}
	return nil
}

func (l *accountLoop) recordAccountAttempt(bookkeepingCtx, credentialCtx context.Context, leased leasedAccount, attemptStarted time.Time, attemptErr error) {
	s, decision := l.s, l.decision
	if leased.managed {
		observability.FromContext(bookkeepingCtx).Info("Subscription account attempt completed",
			"subscription_event", auth.SubscriptionCredentialEventAttempted, "account_id", leased.lease.AccountID,
			"provider", decision.Provider, "succeeded", attemptErr == nil,
			"upstream_status", upstreamStatus(attemptErr), "attempt_ms", s.clock.Now().Sub(attemptStarted).Milliseconds(),
			"response_committed", committed(l.in.Prelude), "subscription_account_attempt", leased.account+1)
	}
	if creds := CredentialsFromContext(credentialCtx); !leased.managed && creds != nil && creds.OAuth && attemptErr != nil {
		s.recordDirectSubscriptionQuota(bookkeepingCtx, decision.Provider, decision.Model, creds, attemptErr)
	}
	if !committed(l.in.Prelude) {
		s.recordSubscriptionModelRejection(bookkeepingCtx, decision.Provider, decision.Model, attemptErr)
	}
}

func (l *accountLoop) recordServed(ctx, credentialCtx, bookkeepingCtx context.Context, leased leasedAccount) {
	s, decision := l.s, l.decision
	if usage := managedSubscriptionUsageOf(ctx); usage != nil {
		usage.WinningCredentials = requestcontext.CredentialsFromContext(credentialCtx)
		usage.Finished = true
	}
	if leased.managed {
		s.markManagedSubscriptionServed(ctx, credentialCtx, leased.lease)
		s.recordManagedSubscriptionSuccess(bookkeepingCtx, decision.Provider, decision.Model, leased.lease)
	}
	if l.attempt.Index > 0 {
		observability.FromContext(ctx).Info("dispatchWithFallback: succeeded on fallback",
			"model", decision.Model,
			"primary_provider", l.in.Bindings[0].Provider,
			"final_provider", decision.Provider,
			"attempt_index", l.attempt.Index)
	}
}

func (l *accountLoop) rotationAfterFailure(bookkeepingCtx, credentialCtx, attemptCtx context.Context, leased leasedAccount, attemptErr error) (context.Context, bool) {
	s, decision := l.s, l.decision
	rotate := leased.managed && (s.recordManagedSubscriptionFailure(bookkeepingCtx, decision.Provider, decision.Model, leased.lease, attemptErr) || providers.IsRetryable(attemptErr))
	if leased.managed && !committed(l.in.Prelude) {
		s.recordSubscriptionUnavailable(bookkeepingCtx, decision.Provider, decision.Model, attemptErr)
	}
	if !leased.managed && !committed(l.in.Prelude) && codexSubscriptionRotates(credentialCtx, attemptErr) {
		s.recordCodexCredentialUnusable(bookkeepingCtx, CredentialsFromContext(credentialCtx), attemptErr)
		s.recordSubscriptionUnavailable(bookkeepingCtx, decision.Provider, decision.Model, attemptErr)
		attemptCtx = resolveAndInjectCredentials(withSuppressedCodexSubscription(attemptCtx), decision.Provider, decision.Model, http.Header{})
		rotate = true
	}
	if next, capped := claudeEnterpriseCapRotation(credentialCtx, attemptCtx, decision, attemptErr); capped && !leased.managed && !committed(l.in.Prelude) {
		attemptCtx, rotate = next, true
	}
	return attemptCtx, rotate
}

func (l *accountLoop) prepareNextAccount(ctx context.Context, leased leasedAccount, attemptErr error) error {
	if l.trace != nil {
		describeAttempt(ctx, l.trace, l.attempt, attemptErr)
		l.trace.BeginAttempt(l.attempt.Target, gatewayDeploymentBaseURL(l.attempt.Target.Provider, l.client), l.s.clock.Now())
	}
	l.lastSubscriptionErr = attemptErr
	if l.rotationBudget.remaining <= 0 {
		return l.s.afterSubscriptionRotationBudget(ctx, l.decision, leased.account+1, attemptErr)
	}
	if l.in.Prelude != nil {
		l.in.Prelude.Discard()
	}
	observability.FromContext(ctx).Warn("dispatchWithFallback: retrying with another subscription account",
		"model", l.decision.Model,
		"provider", l.decision.Provider,
		"account_id", leased.lease.AccountID,
		"same_binding_retry", leased.account+1,
		"err", attemptErr)
	return nil
}
