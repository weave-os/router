package dispatch

import (
	"context"
	"errors"
	"fmt"
	"time"

	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
)

type AttemptFunc func(ctx context.Context, attempt Attempt, client providers.Client) error

type Attempt struct {
	Index            int
	Target           inference.Target
	SameBindingRetry int
}

type Transport struct {
	Attempt     AttemptFunc
	Committed   func() bool
	Reset       func()
	Prepare     func(ctx context.Context, attempt Attempt) (context.Context, error)
	Terminal    func(attempt Attempt, err error) bool
	Bound       func(attempt Attempt, err error) bool
	RetryDelay  func(attempt Attempt, err error, backoff time.Duration) (delay time.Duration, retry bool)
	OperationID string
	Describe    func(attempt Attempt, err error) inference.AttemptDiagnosis
}

type AttemptSink interface {
	RecordAttempt(ctx context.Context, event inference.AttemptEvent)
}

type AttemptSinkFunc func(ctx context.Context, event inference.AttemptEvent)

func (f AttemptSinkFunc) RecordAttempt(ctx context.Context, event inference.AttemptEvent) {
	f(ctx, event)
}

const (
	MaxSameBindingRetries  = 2
	sameBindingBackoffBase = 250 * time.Millisecond
	sameBindingRetryBudget = 10 * time.Second
)

type FailureReason string

const (
	FailureReasonProviderNotConfigured               = "provider_not_configured"
	FailureReasonTargetMismatch                      = "target_mismatch"
	FailureReasonUpstreamStatus                      = "upstream_status"
	FailureReasonUpstreamModelNotFound               = "upstream_model_not_found"
	FailureReasonUpstreamBilling                     = "upstream_billing_blocked"
	FailureReasonTransport                           = "transport"
	FailureReasonCanceled                            = "canceled"
	FailureReasonCommitted                           = "committed"
	FailureReasonPrepare                             = "prepare"
	FailureReasonAttemptBudget                       = "attempt_budget_exhausted"
	FailureReasonAuthorization         FailureReason = "funding_authorization"
)

type Result struct {
	Outcome    inference.ExecutionOutcome
	Summary    inference.OperationSummary
	FinalError error
}

type Executor struct {
	clients *Clients
	sink    AttemptSink
	now     func() time.Time
	sleep   func(context.Context, time.Duration) error
}

type Option func(*Executor)

func WithAttemptSink(sink AttemptSink) Option { return func(e *Executor) { e.sink = sink } }

func WithClock(now func() time.Time) Option { return func(e *Executor) { e.now = now } }

func WithSleep(sleep func(context.Context, time.Duration) error) Option {
	return func(e *Executor) { e.sleep = sleep }
}

func NewExecutor(clients *Clients, opts ...Option) (*Executor, error) {
	if clients == nil {
		return nil, errors.New("dispatch executor requires a client registry")
	}
	e := &Executor{clients: clients, now: time.Now, sleep: sleepWithContext}
	for _, opt := range opts {
		opt(e)
	}
	return e, nil
}

func (e *Executor) Clients() *Clients { return e.clients }

func (e *Executor) Bind(transport Transport) inference.Executor {
	return boundExecutor{executor: e, transport: transport}
}

type boundExecutor struct {
	executor  *Executor
	transport Transport
}

func (b boundExecutor) Execute(ctx context.Context, req inference.InvocationRequest, plan inference.ResolvedPlan) (inference.ExecutionOutcome, error) {
	result, err := b.executor.Run(ctx, req, plan, b.transport)
	return result.Outcome, err
}

func (e *Executor) Run(ctx context.Context, req inference.InvocationRequest, plan inference.ResolvedPlan, transport Transport) (Result, error) {
	if plan == nil {
		return Result{}, errors.New("dispatch: nil plan")
	}
	if transport.Attempt == nil {
		return Result{}, errors.New("dispatch: transport has no attempt")
	}
	targets := append([]inference.Target{plan.SelectedTarget()}, plan.AlternativeTargets()...)
	maxAttempts := plan.Budget().MaxAttempts
	if maxAttempts > 0 && len(targets) > maxAttempts {
		targets = targets[:maxAttempts]
	}
	operationID := transport.OperationID
	if operationID == "" {
		operationID = string(plan.Purpose())
	}
	provenance := inference.Provenance{
		Purpose:          plan.Purpose(),
		PolicyID:         plan.PolicyID(),
		RegistryRevision: inference.PolicyRevision(plan.RegistryRevision()),
		PolicyRevision:   plan.PolicyRevision(),
	}
	result := Result{
		Outcome: inference.ExecutionOutcome{PolicySelectedTarget: plan.SelectedTarget()},
		Summary: inference.OperationSummary{
			Provenance:        provenance,
			PlanTarget:        plan.SelectedTarget(),
			AccountingOutcome: inference.AccountingOutcomeFailed,
		},
	}
	record := func(attempt Attempt, outcome inference.AttemptOutcome, reason string, attemptErr error, started time.Time) {
		result.Outcome.AttemptCount++
		if e.sink == nil && transport.Describe == nil {
			return
		}
		var latency time.Duration
		if !started.IsZero() {
			latency = e.now().Sub(started)
		}
		var diagnosis inference.AttemptDiagnosis
		if transport.Describe != nil {
			diagnosis = transport.Describe(attempt, attemptErr)
		}
		if e.sink == nil {
			return
		}
		status := 0
		if outcome == inference.AttemptOutcomeFailed {
			status = upstreamStatus(attemptErr)
		}
		e.sink.RecordAttempt(ctx, inference.AttemptEvent{
			Provenance:         provenance,
			RequestID:          req.RequestID,
			OperationID:        operationID,
			AttemptIndex:       result.Outcome.AttemptCount - 1,
			Target:             attempt.Target,
			Outcome:            outcome,
			FailureReason:      reason,
			UpstreamStatusCode: status,
			Latency:            latency,
			Diagnosis:          diagnosis,
		})
	}
	fail := func(err error, reason string) (Result, error) {
		result.FinalError = err
		result.Summary.ServedTarget = inference.Target{}
		result.Summary.FallbackReason = reason
		result.Outcome.ResponseCommitted = transport.committed()
		return result, err
	}

	var lastErr error
	authorize := AuthorizationFromContext(ctx)
	for i, target := range targets {
		attempt := Attempt{Index: i, Target: target}
		if i > 0 {
			transport.reset()
		}
		client, err := e.clients.Client(target.Provider)
		if err != nil {
			record(attempt, inference.AttemptOutcomeSkipped, FailureReasonProviderNotConfigured, err, time.Time{})
			lastErr = err
			if i < len(targets)-1 {
				continue
			}
			return fail(err, FailureReasonProviderNotConfigured)
		}
		if authorize != nil {
			client = authorizationGuard{Client: client, target: target, authorize: authorize}
		}

		retryStart := e.now()
		var attemptErr error
		for sb := 0; ; sb++ {
			attempt.SameBindingRetry = sb
			attemptCtx := ctx
			if transport.Prepare != nil {
				prepared, prepErr := transport.Prepare(ctx, attempt)
				if prepErr != nil {
					record(attempt, inference.AttemptOutcomeAborted, FailureReasonPrepare, prepErr, time.Time{})
					transport.reset()
					return fail(prepErr, FailureReasonPrepare)
				}
				attemptCtx = prepared
			}
			started := e.now()
			attemptErr = transport.Attempt(attemptCtx, attempt, client)
			if attemptErr == nil {
				record(attempt, inference.AttemptOutcomeServed, "", nil, started)
				result.Outcome.ServedTarget = target
				result.Outcome.FallbackUsed = i > 0
				result.Outcome.ResponseCommitted = transport.committed()
				result.Summary.ServedTarget = target
				result.Summary.AccountingOutcome = inference.AccountingOutcomeUsageUnknown
				if i > 0 {
					result.Summary.FallbackReason = failureReason(lastErr)
				}
				return result, nil
			}
			lastErr = attemptErr
			reason := failureReason(attemptErr)
			if transport.committed() {
				record(attempt, inference.AttemptOutcomeFailed, reason, attemptErr, started)
				return fail(attemptErr, FailureReasonCommitted)
			}
			record(attempt, inference.AttemptOutcomeFailed, reason, attemptErr, started)
			if errors.Is(attemptErr, ErrTargetMismatch) {
				transport.reset()
				return fail(attemptErr, FailureReasonTargetMismatch)
			}
			if errors.Is(attemptErr, ErrAuthorizationDenied) {
				transport.reset()
				return fail(attemptErr, string(FailureReasonAuthorization))
			}
			if transport.Terminal != nil && transport.Terminal(attempt, attemptErr) {
				break
			}
			if !providers.IsRetryable(attemptErr) ||
				errors.Is(attemptErr, providers.ErrUpstreamIdleTimeout) ||
				errors.Is(attemptErr, providers.ErrUpstreamOutputStall) ||
				errors.Is(attemptErr, providers.ErrUpstreamSlowThroughput) ||
				sb >= MaxSameBindingRetries || len(targets) > 1 {
				break
			}
			if maxAttempts > 0 && result.Outcome.AttemptCount >= maxAttempts {
				break
			}
			spent := e.now().Sub(retryStart)
			if spent >= sameBindingRetryBudget {
				break
			}
			delay := sameBindingBackoffBase << sb
			if transport.RetryDelay != nil {
				chosen, retry := transport.RetryDelay(attempt, attemptErr, delay)
				if !retry || spent+chosen > sameBindingRetryBudget {
					break
				}
				delay = chosen
			}
			transport.reset()
			if err := e.sleep(attemptCtx, delay); err != nil {
				return fail(attemptErr, reason)
			}
		}

		canFailover := providers.IsRetryable(attemptErr) ||
			providers.IsUpstreamModelNotFound(attemptErr) ||
			providers.IsUpstreamProviderBillingBlocked(attemptErr)
		if transport.Terminal != nil && transport.Terminal(attempt, attemptErr) {
			canFailover = false
		}
		if transport.Bound != nil && transport.Bound(attempt, attemptErr) {
			canFailover = false
		}
		if !canFailover || i == len(targets)-1 {
			transport.reset()
			return fail(attemptErr, failureReason(attemptErr))
		}
	}
	return fail(fmt.Errorf("dispatch: %s", FailureReasonAttemptBudget), FailureReasonAttemptBudget)
}

func (t Transport) committed() bool {
	return t.Committed != nil && t.Committed()
}

func (t Transport) reset() {
	if t.Reset != nil {
		t.Reset()
	}
}

func failureReason(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrTargetMismatch):
		return FailureReasonTargetMismatch
	case errors.Is(err, ErrAuthorizationDenied):
		return string(FailureReasonAuthorization)
	case errors.Is(err, ErrProviderNotConfigured):
		return FailureReasonProviderNotConfigured
	case providers.IsUpstreamModelNotFound(err):
		return FailureReasonUpstreamModelNotFound
	case providers.IsUpstreamProviderBillingBlocked(err):
		return FailureReasonUpstreamBilling
	case upstreamStatus(err) != 0:
		return FailureReasonUpstreamStatus
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return FailureReasonCanceled
	default:
		return FailureReasonTransport
	}
}

func upstreamStatus(err error) int {
	var buffered *providers.UpstreamErrorResponse
	if errors.As(err, &buffered) {
		return buffered.Status
	}
	var flushed *providers.UpstreamStatusError
	if errors.As(err, &flushed) {
		return flushed.Status
	}
	return 0
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
