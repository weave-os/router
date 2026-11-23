package proxy

import (
	"context"
	"net/http"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/router/candidates"
	"weave-os/router/internal/subscriptions/entitlement"
)

func fallbackFor(s *Service) dispatch.Fallback {
	return dispatch.Fallback{
		Coverage:    subscriptionCoverage{s},
		Lanes:       subscriptionLanes{s},
		Accounts:    subscriptionAccountPool{s: s},
		Credentials: rescueCredentialPort{s},
		Bindings:    rescueRoutingPort{s},
		Diagnostics: fallbackDiagnostics{s},
		Plans:       s.inferencePlans,
		Executor:    s.inferenceExecutor,
		Now:         s.clock.Now,
	}
}

type subscriptionCoverage struct{ s *Service }

var _ dispatch.SubscriptionCoverage = subscriptionCoverage{}

func (c subscriptionCoverage) Begin(ctx context.Context, in dispatch.FailoverInputs) context.Context {
	ctx = credentials.WithClaudeEnterpriseTurn(ctx, in.InitialDecision)
	return c.s.withSubscriptionFundingAuthorization(ctx, in.SubscriptionStateHeaders)
}

func (subscriptionCoverage) StateModelsEnabled(ctx context.Context) bool {
	return subscriptionStateModelsEnabled(ctx)
}

func (subscriptionCoverage) CoversWhenInactive(ctx context.Context, model string) bool {
	_, allowed := candidates.ModelSet(installationSubscriptionModelsWhenInactiveFromContext(ctx))[model]
	return allowed
}

func (subscriptionCoverage) OverflowEnabled(ctx context.Context) bool {
	return openRouterOverflowEnabled(ctx)
}

func (subscriptionCoverage) AttemptOnly(ctx context.Context) bool {
	return credentials.SubscriptionAttemptOnly(ctx)
}

func (subscriptionCoverage) APIOnly(ctx context.Context) bool {
	return credentials.SubscriptionAPIOnly(ctx)
}

func (subscriptionCoverage) WithAttempt(ctx context.Context) context.Context {
	return withSubscriptionAttempt(ctx)
}

func (subscriptionCoverage) WithAPIOnly(ctx context.Context) context.Context {
	return credentials.WithSuppressedClaudeSubscription(credentials.WithSuppressedCodexSubscription(context.WithValue(ctx, credentials.SubscriptionAPIOnlyKey{}, true)))
}

func (subscriptionCoverage) PaidFallbackForbidden(ctx context.Context) bool {
	return paidFallbackForbidden(ctx)
}

func (subscriptionCoverage) RotationBudgetSpent(ctx context.Context) bool {
	_, budget := credentials.WithSubscriptionAdmissionBudget(ctx)
	return budget.Remaining <= 0
}

func (subscriptionCoverage) NoteIntendedModel(ctx context.Context, model string) {
	if usage := managedSubscriptionUsageOf(ctx); usage != nil {
		usage.IntendedModel = model
	}
}

func (subscriptionCoverage) EligibilityRefusal(ctx context.Context, model string) error {
	return credentials.ProductEligibilityRefusal(ctx, entitlement.ModelBoundaryFromContext(ctx), model)
}

func (subscriptionCoverage) PoolExhausted(ctx context.Context) error {
	return credentials.SubscriptionPoolExhausted(ctx)
}

type subscriptionLanes struct{ s *Service }

var _ dispatch.SubscriptionLanes = subscriptionLanes{}

func (l subscriptionLanes) ServeStateModels(ctx context.Context, in dispatch.FailoverInputs) (int, error) {
	return l.s.dispatchSubscriptionStateModels(ctx, in)
}

func (l subscriptionLanes) WithOverflowTarget(ctx context.Context, in dispatch.FailoverInputs) (context.Context, dispatch.FailoverInputs, error) {
	return l.s.withOpenRouterOverflowTarget(ctx, in)
}

var _ dispatch.FallbackCredentials = rescueCredentialPort{}

func (rescueCredentialPort) ClearCredentials(ctx context.Context) context.Context {
	return credentials.ClearCredentials(ctx)
}

func (rescueCredentialPort) InjectCredentials(ctx context.Context, provider, model string) context.Context {
	return credentials.ResolveAndInjectCredentials(ctx, provider, model, http.Header{})
}

type fallbackDiagnostics struct{ s *Service }

var _ dispatch.FallbackDiagnostics = fallbackDiagnostics{}

func (fallbackDiagnostics) Trace(ctx context.Context) *dispatch.AttemptTrace {
	return attemptTraceFromContext(ctx)
}

func (fallbackDiagnostics) Describe(ctx context.Context, trace *dispatch.AttemptTrace, attempt dispatch.Attempt, err error) inference.AttemptDiagnosis {
	return describeAttempt(ctx, trace, attempt, err)
}

func (fallbackDiagnostics) AttributeAbort(trace *dispatch.AttemptTrace, err error) error {
	return attributeCodexUsageLimitAbort(trace, err)
}

func (fallbackDiagnostics) AttributeTerminal(trace *dispatch.AttemptTrace, err error) error {
	return attributeTerminalError(trace, err)
}

func (d fallbackDiagnostics) RecordOverloadOutcome(ctx context.Context, model, provider string, committed bool, err error) {
	d.s.recordOverloadOutcome(ctx, model, provider, committed, err)
}
