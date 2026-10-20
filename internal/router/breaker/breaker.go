// Package breaker is a per-model circuit breaker fed by upstream overload
// signals. It is pure in-process state with no I/O: each router replica
// observes its own traffic.
//
// A shared circuit pools every organization's evidence about a provider's own
// capacity, but withdraws the model only from organizations that have each
// confirmed the overload with failures of their own. An isolated circuit
// belongs to one organization (traffic through its own gateway) and only ever
// sees that organization's failures.
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

// Config tunes when a circuit opens, who it withdraws the model from, and how
// long it waits before probing.
type Config struct {
	// Strikes overload failures within Window open a circuit.
	Strikes int
	Window  time.Duration
	// OrgConfirmations is how many of its own overload failures an
	// organization must observe before an open shared circuit withdraws the
	// model from it. Failures within Window before the circuit opened count.
	OrgConfirmations int
	// RecoveryBase is the first open period; each failed probe doubles it,
	// up to RecoveryCap.
	RecoveryBase time.Duration
	RecoveryCap  time.Duration
	// ProbeLease defaults to DefaultProbeLease when zero.
	ProbeLease time.Duration
}

// Probe is one claimed half-open slot. The token makes resolving a slot that
// was since resolved or reclaimed a no-op.
type Probe struct {
	Model string
	key   string
	token uint64
}

// Transition reports what a call did to one circuit, for logging.
type Transition struct {
	Model    string
	Org      string
	Isolated bool
	From, To State
	// Confirmed is true when the call made Org confirm an open shared
	// circuit, withdrawing the model from it.
	Confirmed bool
	// Failures is the in-window failure count when a closed circuit opened.
	Failures int
	// RecoveryAfter is the open period that just started; zero otherwise.
	RecoveryAfter time.Duration
}

// Changed reports whether the call moved the circuit or an organization's
// view of it.
func (t Transition) Changed() bool { return t.From != t.To || t.Confirmed }

type circuit struct {
	model string
	// org is set only on isolated circuits.
	org      string
	state    State
	failures []time.Time
	// orgFailures holds each organization's recent failures on a shared
	// circuit, newest last, at most OrgConfirmations per organization.
	orgFailures  map[string][]time.Time
	episodeStart time.Time
	openedAt     time.Time
	reopens      int
	probeToken   uint64
	probeUntil   time.Time
	// offeredAt is when the probe was last claimed; a released probe sorts
	// behind circuits not yet offered so one unwanted model cannot starve
	// the others.
	offeredAt time.Time
}

// Registry holds every circuit on the replica. The per-organization withdrawn
// sets and the earliest instant any circuit becomes due for a probe are
// published atomically and rebuilt only when they change, so request-path
// readers never take the lock until a probe is actually due.
type Registry struct {
	cfg Config
	now func() time.Time

	mu        sync.Mutex
	circuits  map[string]*circuit
	nextToken uint64

	withdrawn atomic.Pointer[map[string]map[string]struct{}]
	// nextDue is the UnixNano at which the soonest circuit becomes claimable;
	// zero when every circuit is closed.
	nextDue atomic.Int64
}

// New returns a registry with every circuit closed. now defaults to time.Now.
func New(cfg Config, now func() time.Time) *Registry {
	cfg.Strikes = max(cfg.Strikes, 1)
	cfg.OrgConfirmations = max(cfg.OrgConfirmations, 1)
	if cfg.RecoveryBase <= 0 {
		cfg.RecoveryBase = time.Second
	}
	cfg.RecoveryCap = max(cfg.RecoveryCap, cfg.RecoveryBase)
	if cfg.ProbeLease <= 0 {
		cfg.ProbeLease = DefaultProbeLease
	}
	if now == nil {
		now = time.Now
	}
	return &Registry{cfg: cfg, now: now, circuits: make(map[string]*circuit)}
}

// Withdrawn returns the models org may not choose automatically: open or
// half-open shared circuits it has confirmed, plus its own open isolated
// circuits. The map is shared and must not be modified; nil when empty.
func (r *Registry) Withdrawn(org string) map[string]struct{} {
	if p := r.withdrawn.Load(); p != nil {
		return (*p)[org]
	}
	return nil
}

// RecordOverload counts one overload failure of model observed by org from a
// request that does not hold the circuit's probe. isolated selects org's own
// circuit for the model instead of the shared one. A closed circuit opens once
// Strikes failures fall within Window; on an open shared circuit the failure
// can only confirm it for org, since only its probe decides recovery.
func (r *Registry) RecordOverload(org, model string, isolated bool) Transition {
	if org == "" || model == "" {
		return Transition{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	r.sweepLocked(now)
	key := circuitKey(org, model, isolated)
	c := r.circuits[key]
	if c == nil {
		c = &circuit{model: model, state: StateClosed}
		if isolated {
			c.org = org
		} else {
			c.orgFailures = make(map[string][]time.Time)
		}
		r.circuits[key] = c
	}
	transition := Transition{Model: model, Org: org, Isolated: isolated, From: c.state, To: c.state}
	confirmedBefore := r.confirmedLocked(c, org, now)
	if !isolated {
		c.orgFailures[org] = recentFailures(append(c.orgFailures[org], now), r.cutoffLocked(c, now), r.cfg.OrgConfirmations)
	}
	if c.state != StateClosed {
		if r.confirmedLocked(c, org, now) && !confirmedBefore {
			transition.Confirmed = true
			r.publishLocked()
		}
		return transition
	}
	c.failures = recentFailures(append(c.failures, now), now.Add(-r.cfg.Window), r.cfg.Strikes)
	if len(c.failures) < r.cfg.Strikes {
		return transition
	}
	transition.Failures = len(c.failures)
	c.state, c.openedAt, c.episodeStart, c.reopens, c.failures = StateOpen, now, now, 0, nil
	transition.To, transition.RecoveryAfter = StateOpen, r.recoveryLocked(0)
	transition.Confirmed = r.confirmedLocked(c, org, now)
	r.publishLocked()
	return transition
}

// ProbeSucceeded closes the probed circuit and resets its whole state,
// including every organization's confirmation. No-op unless p still holds the
// circuit's half-open slot.
func (r *Registry) ProbeSucceeded(p Probe) Transition {
	return r.resolveProbe(p, func(*circuit, time.Time) {
		delete(r.circuits, p.key)
	})
}

// ProbeFailed reopens the probed circuit with a doubled recovery timeout,
// capped at RecoveryCap; confirmations carry over. No-op unless p still holds
// the half-open slot.
func (r *Registry) ProbeFailed(p Probe) Transition {
	return r.resolveProbe(p, func(c *circuit, now time.Time) {
		c.state, c.openedAt, c.reopens = StateOpen, now, c.reopens+1
	})
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

// ClaimProbe grants org the half-open slot of the first due circuit it may
// probe that eligible accepts, preferring circuits whose probe was offered
// least recently, then the longest open. Any organization may probe a shared
// circuit, so one nobody has confirmed still recovers; an isolated circuit is
// probed only by its owner. A circuit is due once its recovery timeout has
// elapsed, or when a previous probe's lease expired unresolved.
func (r *Registry) ClaimProbe(org string, eligible func(model string) bool) (Probe, Transition, bool) {
	nextDue := r.nextDue.Load()
	if nextDue == 0 || r.now().UnixNano() < nextDue {
		return Probe{}, Transition{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	due := make([]string, 0, len(r.circuits))
	for key, c := range r.circuits {
		if at, claimable := r.dueAtLocked(c); claimable && !now.Before(at) && (c.org == "" || c.org == org) {
			due = append(due, key)
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
	for _, key := range due {
		c := r.circuits[key]
		if !eligible(c.model) {
			continue
		}
		from := c.state
		r.nextToken++
		c.state, c.probeToken, c.probeUntil, c.offeredAt = StateHalfOpen, r.nextToken, now.Add(r.cfg.ProbeLease), now
		r.publishLocked()
		probe := Probe{Model: c.model, key: key, token: c.probeToken}
		return probe, Transition{Model: c.model, Org: org, Isolated: c.org != "", From: from, To: StateHalfOpen}, true
	}
	return Probe{}, Transition{}, false
}

// State reports the position of model's shared circuit, or of org's isolated
// circuit for it when isolated is set.
func (r *Registry) State(org, model string, isolated bool) State {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c := r.circuits[circuitKey(org, model, isolated)]; c != nil {
		return c.state
	}
	return StateClosed
}

func (r *Registry) resolveProbe(p Probe, apply func(*circuit, time.Time)) Transition {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.circuits[p.key]
	if c == nil || c.state != StateHalfOpen || c.probeToken != p.token {
		return Transition{Model: p.Model}
	}
	c.probeToken, c.probeUntil = 0, time.Time{}
	apply(c, r.now())
	transition := Transition{Model: p.Model, Org: c.org, Isolated: c.org != "", From: StateHalfOpen, To: StateClosed}
	if _, kept := r.circuits[p.key]; kept {
		transition.To = c.state
	}
	if transition.To == StateOpen {
		transition.RecoveryAfter = r.recoveryLocked(c.reopens)
	}
	r.publishLocked()
	return transition
}

// confirmedLocked reports whether shared circuit c withdraws its model from
// org: only once org has enough failures of its own in the current episode.
// A closed circuit withdraws nothing.
func (r *Registry) confirmedLocked(c *circuit, org string, now time.Time) bool {
	if c.state == StateClosed {
		return false
	}
	if c.org != "" {
		return true
	}
	cutoff := r.cutoffLocked(c, now)
	count := 0
	for _, at := range c.orgFailures[org] {
		if at.After(cutoff) {
			count++
		}
	}
	return count >= r.cfg.OrgConfirmations
}

// cutoffLocked is the oldest failure that still counts toward c: one Window
// before the episode began while it is open, one Window ago while closed.
func (r *Registry) cutoffLocked(c *circuit, now time.Time) time.Time {
	if c.state == StateClosed {
		return now.Add(-r.cfg.Window)
	}
	return c.episodeStart.Add(-r.cfg.Window)
}

// sweepLocked drops closed circuits whose every failure has aged out, so
// one-off strikes from many organizations cannot accumulate.
func (r *Registry) sweepLocked(now time.Time) {
	cutoff := now.Add(-r.cfg.Window)
	for key, c := range r.circuits {
		if c.state != StateClosed || hasFailureAfter(c.failures, cutoff) {
			continue
		}
		stale := true
		for _, failures := range c.orgFailures {
			if hasFailureAfter(failures, cutoff) {
				stale = false
				break
			}
		}
		if stale {
			delete(r.circuits, key)
		}
	}
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
	now := r.now()
	withdrawn := make(map[string]map[string]struct{})
	add := func(org, model string) {
		if withdrawn[org] == nil {
			withdrawn[org] = make(map[string]struct{})
		}
		withdrawn[org][model] = struct{}{}
	}
	var nextDue time.Time
	for _, c := range r.circuits {
		at, claimable := r.dueAtLocked(c)
		if !claimable {
			continue
		}
		if nextDue.IsZero() || at.Before(nextDue) {
			nextDue = at
		}
		if c.org != "" {
			add(c.org, c.model)
			continue
		}
		for org := range c.orgFailures {
			if r.confirmedLocked(c, org, now) {
				add(org, c.model)
			}
		}
	}
	if nextDue.IsZero() {
		r.nextDue.Store(0)
	} else {
		r.nextDue.Store(nextDue.UnixNano())
	}
	if len(withdrawn) == 0 {
		r.withdrawn.Store(nil)
		return
	}
	r.withdrawn.Store(&withdrawn)
}

func circuitKey(org, model string, isolated bool) string {
	if !isolated {
		return model
	}
	return org + "\x00" + model
}

// recentFailures drops failures at or before cutoff and keeps at most limit of
// the newest, so a storm cannot grow the slice.
func recentFailures(failures []time.Time, cutoff time.Time, limit int) []time.Time {
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

func hasFailureAfter(failures []time.Time, cutoff time.Time) bool {
	for _, at := range failures {
		if at.After(cutoff) {
			return true
		}
	}
	return false
}
