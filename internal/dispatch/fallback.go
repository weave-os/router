package dispatch

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"weave-os/router/internal/inference"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
)

var ErrDispatchWithoutPurpose = errors.New("dispatchWithFallback: no inference purpose")

type Abort struct{ Err error }

func (e Abort) Error() string { return e.Err.Error() }
func (e Abort) Unwrap() error { return e.Err }

type SubscriptionCoverage interface {
	Begin(ctx context.Context, in FailoverInputs) context.Context
	StateModelsEnabled(ctx context.Context) bool
	CoversWhenInactive(ctx context.Context, model string) bool
	OverflowEnabled(ctx context.Context) bool
	AttemptOnly(ctx context.Context) bool
	APIOnly(ctx context.Context) bool
	WithAttempt(ctx context.Context) context.Context
	WithAPIOnly(ctx context.Context) context.Context
	PaidFallbackForbidden(ctx context.Context) bool
	RotationBudgetSpent(ctx context.Context) bool
	NoteIntendedModel(ctx context.Context, model string)
	EligibilityRefusal(ctx context.Context, model string) error
	PoolExhausted(ctx context.Context) error
}

type SubscriptionLanes interface {
	ServeStateModels(ctx context.Context, in FailoverInputs) (int, error)
	WithOverflowTarget(ctx context.Context, in FailoverInputs) (context.Context, FailoverInputs, error)
}

type FallbackCredentials interface {
	ResolveCredentials(ctx context.Context, provider, model string, headers http.Header) context.Context
	ClearCredentials(ctx context.Context) context.Context
	InjectCredentials(ctx context.Context, provider, model string) context.Context
}

type BindingResolver interface {
	ResolveBindingsForDispatch(ctx context.Context, decision router.Decision) []catalog.ProviderBinding
}

type FallbackDiagnostics interface {
	Trace(ctx context.Context) *AttemptTrace
	Describe(ctx context.Context, trace *AttemptTrace, attempt Attempt, err error) inference.AttemptDiagnosis
	AttributeAbort(trace *AttemptTrace, err error) error
	AttributeTerminal(trace *AttemptTrace, err error) error
	RecordOverloadOutcome(ctx context.Context, model, provider string, committed bool, err error)
}

type Fallback struct {
	Coverage    SubscriptionCoverage
	Lanes       SubscriptionLanes
	Accounts    SubscriptionAccountPool
	Credentials FallbackCredentials
	Bindings    BindingResolver
	Diagnostics FallbackDiagnostics
	Plans       func() (*policy.PlanResolver, error)
	Executor    func() (*Executor, error)
	Now         func() time.Time
}

func preludeCommitted(b *PreludeBuffer) bool {
	return b != nil && b.Committed()
}

func (f Fallback) Dispatch(ctx context.Context, in FailoverInputs) (winnerIdx int, err error) {
	ctx = f.Coverage.Begin(ctx, in)
	if in.SubscriptionStateRequest != nil && f.Coverage.StateModelsEnabled(ctx) && in.BuildAlternative != nil {
		return f.Lanes.ServeStateModels(ctx, in)
	}
	if f.Coverage.StateModelsEnabled(ctx) && !f.Coverage.AttemptOnly(ctx) {
		if !f.Coverage.CoversWhenInactive(ctx, in.InitialDecision.Model) {
			return -1, router.ErrAllowlistEmptiesPool
		}
		if f.Coverage.OverflowEnabled(ctx) && f.Coverage.APIOnly(ctx) {
			var err error
			if ctx, in, err = f.Lanes.WithOverflowTarget(ctx, in); err != nil {
				return -1, err
			}
		}
	}
	if len(in.Alternatives) > 0 && in.BuildAlternative != nil {
		return f.dispatchAlternatives(ctx, in)
	}
	if len(in.Purpose) == 0 {
		return -1, ErrDispatchWithoutPurpose
	}
	if err := f.Coverage.EligibilityRefusal(ctx, in.InitialDecision.Model); err != nil {
		return -1, err
	}
	if len(in.Bindings) == 0 {
		return -1, unboundDispatchError(in)
	}
	plans, err := f.Plans()
	if err != nil {
		return -1, err
	}
	plan, err := plans.ResolveRouted(policy.RoutedResolutionRequest{
		Purpose:  policy.Purpose(in.Purpose),
		Decision: in.InitialDecision,
		Bindings: in.Bindings,
		Origin:   in.Origin,
	})
	if err != nil {
		return -1, err
	}
	return f.dispatchPlanned(ctx, in, plan)
}

func unboundDispatchError(in FailoverInputs) error {
	if !in.InitialDecision.UserForcedModel() {
		return &providers.UpstreamStatusError{Status: http.StatusBadGateway}
	}
	err := &providers.UpstreamErrorResponse{
		Status: http.StatusServiceUnavailable,
		Body: []byte(fmt.Sprintf(`{"error":{"message":%q,"type":"api_error"}}`,
			fmt.Sprintf("forced model %s unavailable: provider %s not configured", in.InitialDecision.Model, in.InitialDecision.Provider))),
	}
	if in.FlushErr != nil && !in.DeferFlushOnExhaustion {
		in.FlushErr(in.Writer, err)
	}
	return err
}

func (f Fallback) dispatchAlternatives(ctx context.Context, in FailoverInputs) (int, error) {
	selected := in.InitialDecision
	f.Coverage.NoteIntendedModel(ctx, selected.Model)
	targets := append([]router.Decision{selected}, in.Alternatives...)
	for index, target := range targets {
		if ctx.Err() != nil || f.Coverage.RotationBudgetSpent(ctx) {
			break
		}
		attemptIn := in
		attemptIn.Alternatives = nil
		attemptIn.DeferFlushOnExhaustion = true
		if index > 0 {
			attempt, buildErr := in.BuildAlternative(target)
			if buildErr != nil {
				continue
			}
			attemptIn.InitialDecision = target
			attemptIn.Attempt = attempt
			attemptIn.Bindings = f.Bindings.ResolveBindingsForDispatch(ctx, target)
		}
		if in.Prelude != nil {
			in.Prelude.Discard()
		}
		attemptCtx := f.Coverage.WithAttempt(ctx)
		if target.Provider != selected.Provider {
			attemptCtx = f.Credentials.ClearCredentials(attemptCtx)
		}
		attemptCtx = f.Credentials.ResolveCredentials(attemptCtx, target.Provider, target.Model, http.Header{})
		winner, attemptErr := f.Dispatch(attemptCtx, attemptIn)
		if attemptErr == nil {
			if index > 0 && in.OnAlternative != nil {
				in.OnAlternative(target)
			}
			return winner, nil
		}
		if preludeCommitted(in.Prelude) || ctx.Err() != nil {
			return winner, attemptErr
		}
	}
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	if f.Coverage.PaidFallbackForbidden(ctx) {
		return 0, f.Coverage.PoolExhausted(ctx)
	}
	if in.Prelude != nil {
		in.Prelude.Discard()
	}
	in.Alternatives = nil
	apiCtx := f.Credentials.ResolveCredentials(f.Coverage.WithAPIOnly(ctx), selected.Provider, selected.Model, http.Header{})
	return f.Dispatch(apiCtx, in)
}

func (f Fallback) dispatchPlanned(ctx context.Context, in FailoverInputs, plan inference.ResolvedPlan) (winnerIdx int, err error) {
	executor, err := f.Executor()
	if err != nil {
		return -1, err
	}

	lastIdx := 0
	lastProvider := in.InitialDecision.Provider
	ctx, accounts := f.Accounts.Begin(ctx, in.InitialDecision)
	transport := Transport{
		OperationID: string(in.Purpose),
		Committed:   func() bool { return preludeCommitted(in.Prelude) },
		Reset: func() {
			if in.Prelude != nil {
				in.Prelude.Discard()
			}
		},
		Prepare: func(ctx context.Context, attempt Attempt) (context.Context, error) {
			lastIdx = attempt.Index
			lastProvider = attempt.Target.Provider
			if attempt.Index == 0 {
				return ctx, nil
			}
			return f.Credentials.InjectCredentials(ctx, attempt.Target.Provider, in.InitialDecision.Model), nil
		},
		Terminal: func(_ Attempt, err error) bool {
			var abort Abort
			return errors.As(err, &abort)
		},
		Bound:      func(Attempt, error) bool { return accounts.Bound() },
		RetryDelay: ThrottleLedgerFromContext(ctx).RetryDelay(f.Now),
	}
	transport.Attempt = func(attemptCtx context.Context, attempt Attempt, client providers.Client) error {
		return accounts.Serve(ctx, attemptCtx, AccountAttempt{Inputs: in, Plan: plan, Attempt: attempt, Client: client})
	}

	if trace := f.Diagnostics.Trace(ctx); trace != nil {
		transport.Describe = func(attempt Attempt, err error) inference.AttemptDiagnosis {
			return f.Diagnostics.Describe(ctx, trace, attempt, err)
		}
	}
	req := inference.InvocationRequest{Purpose: in.Purpose, RequestID: observability.RequestIDFromContext(ctx)}
	result, runErr := executor.Run(ctx, req, plan, transport)
	f.Diagnostics.RecordOverloadOutcome(ctx, in.InitialDecision.Model, lastProvider, result.Outcome.ResponseCommitted, runErr)
	if runErr == nil {
		return lastIdx, nil
	}
	var abort Abort
	switch reason := result.Summary.FallbackReason; {
	case errors.As(runErr, &abort):
		runErr = f.Diagnostics.AttributeAbort(f.Diagnostics.Trace(ctx), abort.Err)
	case reason == FailureReasonProviderNotConfigured, reason == FailureReasonPrepare:
		return lastIdx, runErr
	default:
		runErr = f.Diagnostics.AttributeTerminal(f.Diagnostics.Trace(ctx), runErr)
	}
	if in.FlushErr != nil && !in.DeferFlushOnExhaustion && !preludeCommitted(in.Prelude) {
		in.FlushErr(in.Writer, runErr)
	}
	return lastIdx, runErr
}
