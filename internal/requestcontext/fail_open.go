package requestcontext

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Dependency names a bounded internal prerequisite, not an upstream provider.
type Dependency uint8

const (
	DependencyDatabase Dependency = iota
	DependencyPolicy
	DependencyAuxiliary
	DependencyPreparation
)

// FailOpenReason is the bounded vocabulary exposed in response metadata.
type FailOpenReason string

const (
	ReasonDatabaseUnavailable  FailOpenReason = "database_unavailable"
	ReasonPolicyUnavailable    FailOpenReason = "policy_unavailable"
	ReasonAuxiliaryUnavailable FailOpenReason = "auxiliary_unavailable"
	ReasonPreparationDeadline  FailOpenReason = "preparation_deadline"
)

// ErrDependencyUnavailable distinguishes infrastructure failure from a denial.
var ErrDependencyUnavailable = errors.New("router dependency unavailable")

// DependencyError carries a bounded diagnostic reason without request content.
type DependencyError struct {
	Dependency Dependency
	Cause      error
}

// Error reports the prerequisite and its underlying failure.
func (e *DependencyError) Error() string { return fmt.Sprintf("%s: %v", e.Reason(), e.Cause) }

// Unwrap exposes the underlying infrastructure error.
func (e *DependencyError) Unwrap() error { return e.Cause }

// Is recognizes dependency unavailability independently of its cause.
func (e *DependencyError) Is(target error) bool { return target == ErrDependencyUnavailable }

// Reason is suitable for response metadata and low-cardinality metrics.
func (e *DependencyError) Reason() FailOpenReason {
	switch e.Dependency {
	case DependencyDatabase:
		return ReasonDatabaseUnavailable
	case DependencyPolicy:
		return ReasonPolicyUnavailable
	case DependencyPreparation:
		return ReasonPreparationDeadline
	default:
		return ReasonAuxiliaryUnavailable
	}
}

// PreparationLimits bound all prerequisite work for one request. Provider I/O
// retains the original client's deadline instead of this preparation deadline.
type PreparationLimits struct {
	Total        time.Duration
	Database     time.Duration
	DatabaseCall time.Duration
	Cooldown     time.Duration
}

// DefaultPreparationLimits are deployment defaults, not measured latency SLOs.
func DefaultPreparationLimits() PreparationLimits {
	return PreparationLimits{Total: 12 * time.Second, Database: time.Second, DatabaseCall: 250 * time.Millisecond, Cooldown: 5 * time.Second}
}

// DependencyHealth suppresses repeated doomed calls and admits one recovery
// probe per dependency after its cooldown. Share it across requests.
type DependencyHealth struct {
	mu               sync.Mutex
	unavailableUntil map[Dependency]time.Time
	probing          map[Dependency]bool
}

// NewDependencyHealth starts with every dependency available for normal calls.
func NewDependencyHealth() *DependencyHealth {
	return &DependencyHealth{unavailableUntil: make(map[Dependency]time.Time), probing: make(map[Dependency]bool)}
}

func (h *DependencyHealth) acquire(dependency Dependency, now time.Time) (bool, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	until, unavailable := h.unavailableUntil[dependency]
	if !unavailable {
		return true, false
	}
	if now.Before(until) || h.probing[dependency] {
		return false, false
	}
	h.probing[dependency] = true
	return true, true
}

func (h *DependencyHealth) finish(dependency Dependency, err error, cooldown time.Duration, probe, canceled bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if canceled {
		if probe {
			delete(h.probing, dependency)
		}
		return
	}
	if err != nil {
		h.unavailableUntil[dependency] = time.Now().Add(cooldown)
	} else if probe {
		delete(h.unavailableUntil, dependency)
	}
	if probe {
		delete(h.probing, dependency)
	}
}

type preparationKey struct{}

// Preparation owns the aggregate budget and first failure of one request.
// It deliberately contains no credentials, tenant identity, or request body.
type Preparation struct {
	mu              sync.Mutex
	live            context.Context
	deadline        time.Time
	cancel          context.CancelFunc
	health          *DependencyHealth
	limits          PreparationLimits
	databaseSpent   time.Duration
	failure         *DependencyError
	providerStarted bool
}

// BeginPreparation is idempotent across nested protocol adapters and middleware.
func BeginPreparation(ctx context.Context, health *DependencyHealth, limits PreparationLimits) (context.Context, *Preparation, bool) {
	if state := PreparationFrom(ctx); state != nil {
		return ctx, state, false
	}
	prepared, cancel := context.WithTimeout(ctx, limits.Total)
	deadline, _ := prepared.Deadline()
	state := &Preparation{live: ctx, deadline: deadline, cancel: cancel, health: health, limits: limits}
	return context.WithValue(prepared, preparationKey{}, state), state, true
}

// PreparationFrom returns nil when dependency fail-open is disabled.
func PreparationFrom(ctx context.Context) *Preparation {
	state, _ := ctx.Value(preparationKey{}).(*Preparation)
	return state
}

// Close releases the preparation timer without cancelling the client request.
func (p *Preparation) Close() { p.cancel() }

// Failure returns the first prerequisite failure, if any.
func (p *Preparation) Failure() *DependencyError {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.failure
}

// Fail latches one dependency failure. Caller cancellation is not recoverable.
func (p *Preparation) Fail(dependency Dependency, err error) error {
	if p.live.Err() != nil {
		return p.live.Err()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failure == nil {
		p.failure = &DependencyError{Dependency: dependency, Cause: err}
		p.cancel()
	}
	return p.failure
}

// Start bounds a prerequisite call. Call finish exactly once with its result.
// A nil state preserves the legacy context and does not change error handling.
// StartDependency starts a bounded operation when request preparation is active.
func StartDependency(ctx context.Context, dependency Dependency) (context.Context, func(error), error) {
	preparation := PreparationFrom(ctx)
	if preparation == nil {
		return ctx, func(error) {}, nil
	}
	return preparation.Start(ctx, dependency)
}

func (p *Preparation) Start(ctx context.Context, dependency Dependency) (context.Context, func(error), error) {
	if p == nil {
		return ctx, func(error) {}, nil
	}
	if failure := p.Failure(); failure != nil {
		return ctx, func(error) {}, failure
	}
	p.mu.Lock()
	startedProvider := p.providerStarted
	p.mu.Unlock()
	if startedProvider {
		return ctx, func(error) {}, nil
	}
	if err := p.live.Err(); err != nil {
		return ctx, func(error) {}, err
	}
	if !time.Now().Before(p.deadline) {
		return ctx, func(error) {}, p.Fail(DependencyPreparation, context.DeadlineExceeded)
	}
	admitted, probe := p.health.acquire(dependency, time.Now())
	if !admitted {
		return ctx, func(error) {}, p.Fail(dependency, ErrDependencyUnavailable)
	}
	started := time.Now()
	callCtx, cancel := context.WithDeadline(ctx, p.deadline)
	if dependency == DependencyDatabase {
		cancel()
		p.mu.Lock()
		remaining := p.limits.Database - p.databaseSpent
		p.mu.Unlock()
		callCtx, cancel = context.WithTimeout(ctx, min(remaining, p.limits.DatabaseCall, time.Until(p.deadline)))
	}
	finish := func(err error) {
		cancel()
		if dependency == DependencyDatabase {
			p.mu.Lock()
			p.databaseSpent += time.Since(started)
			p.mu.Unlock()
		}
		if p.live.Err() != nil {
			p.health.finish(dependency, nil, p.limits.Cooldown, probe, true)
			return
		}
		p.health.finish(dependency, err, p.limits.Cooldown, probe, false)
		if err != nil {
			p.Fail(dependency, err)
		}
	}
	return callCtx, finish, nil
}

// ProviderStarted prevents a dependency failure from replaying an upstream call.
func (p *Preparation) ProviderStarted() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.providerStarted = true
}

// CanRelay forbids replay after upstream dispatch or caller cancellation.
func (p *Preparation) CanRelay() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.providerStarted && p.live.Err() == nil
}

type liveProviderContext struct {
	context.Context
	live context.Context
}

func (c liveProviderContext) Deadline() (time.Time, bool) { return c.live.Deadline() }
func (c liveProviderContext) Done() <-chan struct{}       { return c.live.Done() }
func (c liveProviderContext) Err() error                  { return c.live.Err() }

// ProviderContext retains resolved request values but restores the live client
// cancellation domain. It never detaches an upstream call from its caller.
func ProviderContext(ctx context.Context) context.Context {
	state := PreparationFrom(ctx)
	if state == nil {
		return ctx
	}
	return liveProviderContext{Context: ctx, live: state.live}
}
