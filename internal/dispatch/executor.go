package dispatch

import (
	"context"
	"errors"
	"fmt"
	"time"

	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
)

// AttemptFunc performs one upstream attempt against client for target. It
// must not fall back on its own: returning the upstream error unmodified lets
// the executor apply the retry and failover rules.
type AttemptFunc func(ctx context.Context, attempt Attempt, client providers.Client) error

// Attempt identifies one upstream attempt of an operation.
type Attempt struct {
	Index  int
	Target inference.Target
	// SameBindingRetry is >0 when the executor re-tries the same target after
	// a transient error.
	SameBindingRetry int
}

// Transport is the per-operation adapter between the executor and the
// response sink. Committed reports whether response bytes already reached the
// client (retry is then forbidden); Reset drops buffered pre-commit output and
// header mutations before another attempt writes. Both are optional.
type Transport struct {
	Attempt   AttemptFunc
	Committed func() bool
	Reset     func()
	// Prepare, when set, runs before each attempt's context is handed to the
	// adapter (credential injection, per-attempt headers). Returning an error
	// aborts the operation.
	Prepare func(ctx context.Context, attempt Attempt) (context.Context, error)
	// OperationID distinguishes this operation from others in the same
	// request; empty means the purpose is used.
	OperationID string
}

// AttemptSink receives ordered attempt events. Sinks must not block dispatch
// on persistence failures.
type AttemptSink interface {
	RecordAttempt(ctx context.Context, event inference.AttemptEvent)
}

// AttemptSinkFunc adapts a function to AttemptSink.
type AttemptSinkFunc func(ctx context.Context, event inference.AttemptEvent)

func (f AttemptSinkFunc) RecordAttempt(ctx context.Context, event inference.AttemptEvent) {
	f(ctx, event)
}

const (
	// maxSameBindingRetries bounds same-binding retries after a transient
	// error; the only failover a single-target plan gets.
	maxSameBindingRetries  = 2
	sameBindingBackoffBase = 250 * time.Millisecond
	// sameBindingRetryBudget caps wall-clock across retries of one target so a
	// hung upstream does not burn a full header timeout per attempt.
	sameBindingRetryBudget = 10 * time.Second
)

// Bounded failure reasons recorded on attempt events.
const (
	FailureReasonProviderNotConfigured = "provider_not_configured"
	FailureReasonTargetMismatch        = "target_mismatch"
	FailureReasonUpstreamStatus        = "upstream_status"
	FailureReasonUpstreamModelNotFound = "upstream_model_not_found"
	FailureReasonUpstreamBilling       = "upstream_billing_blocked"
	FailureReasonTransport             = "transport"
	FailureReasonCanceled              = "canceled"
	FailureReasonCommitted             = "committed"
	FailureReasonPrepare               = "prepare"
	FailureReasonAttemptBudget         = "attempt_budget_exhausted"
)

// Result is the executor's report for one operation.
type Result struct {
	Outcome inference.ExecutionOutcome
	Summary inference.OperationSummary
	// FinalError is the error of the last attempt when the operation failed.
	FinalError error
}

// Executor walks a plan's targets with the shared retry/failover rules.
type Executor struct {
	clients *Clients
	sink    AttemptSink
	now     func() time.Time
	sleep   func(context.Context, time.Duration) error
}

// Option configures an Executor.
type Option func(*Executor)

// WithAttemptSink records ordered attempt events.
func WithAttemptSink(sink AttemptSink) Option { return func(e *Executor) { e.sink = sink } }

// WithClock injects the clock used for retry budgets.
func WithClock(now func() time.Time) Option { return func(e *Executor) { e.now = now } }

// WithSleep injects the backoff wait.
func WithSleep(sleep func(context.Context, time.Duration) error) Option {
	return func(e *Executor) { e.sleep = sleep }
}

// NewExecutor builds the executor over the boot-time client registry.
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

// Clients exposes the registry so composition can share it with callers that
// only need provider presence (eligibility filters, admin views).
func (e *Executor) Clients() *Clients { return e.clients }

// Bind returns an inference.Executor whose Execute drives transport for every
// attempt. The bound value is per operation and must not be reused.
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

// Run executes plan for req: the selected target first, then each policy
// alternative in order. A target is retried in place only when it is the sole
// target and the error is transient; otherwise a retryable, model-not-found,
// or billing-blocked error fails over to the next target while nothing has
// been committed to the client. Budget().MaxAttempts, when positive, bounds
// the total attempts including same-target retries. Attempt events are
// emitted in order.
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
	record := func(attempt Attempt, outcome inference.AttemptOutcome, reason string, status int, started time.Time) {
		result.Outcome.AttemptCount++
		if e.sink == nil {
			return
		}
		var latency time.Duration
		if !started.IsZero() {
			latency = e.now().Sub(started)
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
	for i, target := range targets {
		attempt := Attempt{Index: i, Target: target}
		if i > 0 {
			transport.reset()
		}
		client, err := e.clients.Client(target.Provider)
		if err != nil {
			record(attempt, inference.AttemptOutcomeSkipped, FailureReasonProviderNotConfigured, 0, time.Time{})
			lastErr = err
			if i < len(targets)-1 {
				continue
			}
			return fail(err, FailureReasonProviderNotConfigured)
		}

		retryStart := e.now()
		var attemptErr error
		for sb := 0; ; sb++ {
			attempt.SameBindingRetry = sb
			attemptCtx := ctx
			if transport.Prepare != nil {
				prepared, prepErr := transport.Prepare(ctx, attempt)
				if prepErr != nil {
					record(attempt, inference.AttemptOutcomeAborted, FailureReasonPrepare, 0, time.Time{})
					transport.reset()
					return fail(prepErr, FailureReasonPrepare)
				}
				attemptCtx = prepared
			}
			started := e.now()
			attemptErr = transport.Attempt(attemptCtx, attempt, client)
			if attemptErr == nil {
				record(attempt, inference.AttemptOutcomeServed, "", 0, started)
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
				record(attempt, inference.AttemptOutcomeFailed, reason, upstreamStatus(attemptErr), started)
				return fail(attemptErr, FailureReasonCommitted)
			}
			record(attempt, inference.AttemptOutcomeFailed, reason, upstreamStatus(attemptErr), started)
			if errors.Is(attemptErr, ErrTargetMismatch) {
				transport.reset()
				return fail(attemptErr, FailureReasonTargetMismatch)
			}
			if !providers.IsRetryable(attemptErr) || sb >= maxSameBindingRetries || len(targets) > 1 {
				break
			}
			if maxAttempts > 0 && result.Outcome.AttemptCount >= maxAttempts {
				break
			}
			if e.now().Sub(retryStart) >= sameBindingRetryBudget {
				break
			}
			transport.reset()
			if err := e.sleep(attemptCtx, sameBindingBackoffBase<<sb); err != nil {
				return fail(attemptErr, reason)
			}
		}

		canFailover := providers.IsRetryable(attemptErr) ||
			providers.IsUpstreamModelNotFound(attemptErr) ||
			providers.IsUpstreamProviderBillingBlocked(attemptErr)
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

// failureReason maps an attempt error to a bounded machine reason.
func failureReason(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrTargetMismatch):
		return FailureReasonTargetMismatch
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
