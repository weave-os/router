// Package breaker is a per-model circuit breaker fed by upstream overload
// signals. It is pure in-process state with no I/O: each router replica
// observes its own traffic and opens independently.
package breaker

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// State is one circuit's position in the closed -> open -> half-open cycle.
type State string

const (
	// StateClosed lets the model be chosen normally.
	StateClosed State = "closed"
	// StateOpen withdraws the model until its recovery timeout elapses.
	StateOpen State = "open"
	// StateHalfOpen admits exactly one probe to test recovery.
	StateHalfOpen State = "half_open"
)

// DefaultProbeLease bounds how long a claimed probe may stay unresolved before
// another request may claim it; it covers a long streamed turn.
const DefaultProbeLease = 15 * time.Minute

// Config tunes when a circuit opens and how long it waits before probing.
type Config struct {
	// Strikes overload failures within Window open the circuit.
	Strikes int
	Window  time.Duration
	// RecoveryBase is the first open period; each failed probe doubles it,
	// up to RecoveryCap.
	RecoveryBase time.Duration
	RecoveryCap  time.Duration
	// ProbeLease defaults to DefaultProbeLease when zero.
	ProbeLease time.Duration
}

// Probe is one claimed half-open slot. The token makes releasing a slot that
// was since resolved or reclaimed a no-op.
type Probe struct {
	Model string
	token uint64
}

// Transition reports what a call did to one circuit, for logging.
type Transition struct {
	Model    string
	From, To State
	// Failures is the in-window failure count when a closed circuit opened.
	Failures int
	// RecoveryAfter is the open period that just started; zero otherwise.
	RecoveryAfter time.Duration
}

// Changed reports whether the call moved the circuit.
func (t Transition) Changed() bool { return t.From != t.To }

type circuit struct {
	state      State
	failures   []time.Time
	openedAt   time.Time
	reopens    int
	probeToken uint64
	probeUntil time.Time
	// offeredAt is when the probe was last claimed; a released probe sorts
	// behind circuits not yet offered so one unwanted model cannot starve
	// the others.
	offeredAt time.Time
}

// Registry holds one circuit per model. The open set and the earliest instant
// any circuit becomes due for a probe are published atomically and rebuilt
// only on transitions, so request-path readers never take the lock until a
// probe is actually due.
type Registry struct {
	cfg Config
	now func() time.Time

	mu        sync.Mutex
	circuits  map[string]*circuit
	nextToken uint64

	open atomic.Pointer[map[string]struct{}]
	// nextDue is the UnixNano at which the soonest circuit becomes claimable.
	nextDue atomic.Int64
}

// New returns a registry with every circuit closed. now defaults to time.Now.
func New(cfg Config, now func() time.Time) *Registry {
	if cfg.Strikes < 1 {
		cfg.Strikes = 1
	}
	if cfg.RecoveryBase <= 0 {
		cfg.RecoveryBase = time.Second
	}
	if cfg.RecoveryCap < cfg.RecoveryBase {
		cfg.RecoveryCap = cfg.RecoveryBase
	}
	if cfg.ProbeLease <= 0 {
		cfg.ProbeLease = DefaultProbeLease
	}
	if now == nil {
		now = time.Now
	}
	return &Registry{cfg: cfg, now: now, circuits: make(map[string]*circuit)}
}

// OpenModels returns the models currently withdrawn (open or half-open). The
// map is shared and must not be modified; nil when every circuit is closed.
func (r *Registry) OpenModels() map[string]struct{} {
	if p := r.open.Load(); p != nil {
		return *p
	}
	return nil
}

// RecordOverload counts one overload failure of model from a request that does
// not hold its probe. A closed circuit opens once Strikes failures fall within
// Window; an open or half-open circuit is unchanged, since only its probe
// decides recovery.
func (r *Registry) RecordOverload(model string) Transition {
	if model == "" {
		return Transition{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	c := r.circuits[model]
	if c == nil {
		c = &circuit{state: StateClosed}
		r.circuits[model] = c
	}
	if c.state != StateClosed {
		return Transition{Model: model, From: c.state, To: c.state}
	}
	c.failures = withinWindow(append(c.failures, now), now, r.cfg.Window, r.cfg.Strikes)
	if len(c.failures) < r.cfg.Strikes {
		return Transition{Model: model, From: StateClosed, To: StateClosed}
	}
	failures := len(c.failures)
	c.state, c.openedAt, c.reopens, c.failures = StateOpen, now, 0, nil
	r.publishLocked()
	return Transition{Model: model, From: StateClosed, To: StateOpen, Failures: failures, RecoveryAfter: r.recoveryLocked(0)}
}

// ProbeSucceeded closes the probed circuit and resets its whole state. No-op
// unless p still holds the circuit's half-open slot.
func (r *Registry) ProbeSucceeded(p Probe) Transition {
	return r.resolveProbe(p, func(c *circuit, _ time.Time) {
		delete(r.circuits, p.Model)
	})
}

// ProbeFailed reopens the probed circuit with a doubled recovery timeout,
// capped at RecoveryCap. No-op unless p still holds the half-open slot.
func (r *Registry) ProbeFailed(p Probe) Transition {
	return r.resolveProbe(p, func(c *circuit, now time.Time) {
		c.state, c.openedAt, c.reopens = StateOpen, now, c.reopens+1
	})
}

// ClaimProbe grants the half-open slot of the first due circuit that eligible
// accepts, preferring circuits whose probe was offered least recently, then the
// longest open. A circuit is due once its recovery timeout has elapsed, or when
// a previous probe's lease expired unresolved.
func (r *Registry) ClaimProbe(eligible func(model string) bool) (Probe, Transition, bool) {
	if len(r.OpenModels()) == 0 || r.now().UnixNano() < r.nextDue.Load() {
		return Probe{}, Transition{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	due := make([]string, 0, len(r.circuits))
	for model, c := range r.circuits {
		if at, claimable := r.dueAtLocked(c); claimable && !now.Before(at) {
			due = append(due, model)
		}
	}
	sort.Slice(due, func(i, j int) bool {
		a, b := r.circuits[due[i]], r.circuits[due[j]]
		if !a.offeredAt.Equal(b.offeredAt) {
			return a.offeredAt.Before(b.offeredAt)
		}
		if !a.openedAt.Equal(b.openedAt) {
			return a.openedAt.Before(b.openedAt)
		}
		return due[i] < due[j]
	})
	for _, model := range due {
		if !eligible(model) {
			continue
		}
		c := r.circuits[model]
		from := c.state
		r.nextToken++
		c.state, c.probeToken, c.probeUntil, c.offeredAt = StateHalfOpen, r.nextToken, now.Add(r.cfg.ProbeLease), now
		r.publishLocked()
		return Probe{Model: model, token: c.probeToken}, Transition{Model: model, From: from, To: StateHalfOpen}, true
	}
	return Probe{}, Transition{}, false
}

// ReleaseProbe returns a slot whose request ended without resolving it (the
// model was not dispatched, or failed for a reason other than overload). The
// circuit goes back to open and stays due, behind any due circuit not yet
// offered. No-op when the probe was already resolved or reclaimed.
func (r *Registry) ReleaseProbe(p Probe) Transition {
	return r.resolveProbe(p, func(c *circuit, _ time.Time) {
		c.state = StateOpen
	})
}

func (r *Registry) resolveProbe(p Probe, apply func(*circuit, time.Time)) Transition {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.circuits[p.Model]
	if c == nil || c.state != StateHalfOpen || c.probeToken != p.token {
		return Transition{Model: p.Model}
	}
	c.probeToken, c.probeUntil = 0, time.Time{}
	apply(c, r.now())
	t := Transition{Model: p.Model, From: StateHalfOpen, To: StateClosed}
	if _, kept := r.circuits[p.Model]; kept {
		t.To = c.state
	}
	if t.To == StateOpen {
		t.RecoveryAfter = r.recoveryLocked(c.reopens)
	}
	r.publishLocked()
	return t
}

// State reports model's current position, for logs and tests.
func (r *Registry) State(model string) State {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c := r.circuits[model]; c != nil {
		return c.state
	}
	return StateClosed
}

func (r *Registry) recoveryLocked(reopens int) time.Duration {
	d := r.cfg.RecoveryBase
	for i := 0; i < reopens && d < r.cfg.RecoveryCap; i++ {
		d *= 2
	}
	return min(d, r.cfg.RecoveryCap)
}

// dueAtLocked is when c may next be claimed for a probe.
func (r *Registry) dueAtLocked(c *circuit) (time.Time, bool) {
	switch c.state {
	case StateOpen:
		return c.openedAt.Add(r.recoveryLocked(c.reopens)), true
	case StateHalfOpen:
		return c.probeUntil, true
	}
	return time.Time{}, false
}

func (r *Registry) publishLocked() {
	open := make(map[string]struct{})
	var nextDue time.Time
	for model, c := range r.circuits {
		at, claimable := r.dueAtLocked(c)
		if !claimable {
			continue
		}
		open[model] = struct{}{}
		if nextDue.IsZero() || at.Before(nextDue) {
			nextDue = at
		}
	}
	r.nextDue.Store(nextDue.UnixNano())
	if len(open) == 0 {
		r.open.Store(nil)
		return
	}
	r.open.Store(&open)
}

// withinWindow drops failures older than window and keeps at most limit of
// the newest, so a storm cannot grow the slice.
func withinWindow(failures []time.Time, now time.Time, window time.Duration, limit int) []time.Time {
	cutoff := now.Add(-window)
	kept := failures[:0]
	for _, at := range failures {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}
	if len(kept) > limit {
		kept = kept[len(kept)-limit:]
	}
	return kept
}
