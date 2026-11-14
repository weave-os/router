// Package breaker is a per-model circuit breaker fed by upstream overload
// signals. It is pure in-process state with no I/O: each router replica
// observes its own traffic.
//
// A shared circuit pools every organization's evidence about a provider's own
// capacity, but withdraws the model only from organizations that have each
// confirmed the overload with failures of their own. An isolated circuit
// belongs to one organization (traffic through its own gateway) and only ever
// sees that organization's failures.
//
// Request-path calls never take the lock: Withdrawn and ClaimProbe read an
// atomically published snapshot, and claiming or releasing a probe is one
// compare-and-swap on the circuit. Only overload failures and probe outcomes,
// which change what the snapshot says, lock and republish.
package breaker

import (
	"slices"
	"strings"
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
	// StateHalfOpen is an open circuit whose single probe is in flight.
	StateHalfOpen State = "half_open"
)

// Config tunes when a circuit opens, who it withdraws the model from, and how
// long it waits before probing. Production uses Defaults.
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
}

// Probe is one claimed half-open slot. Resolving or releasing a slot that
// was since resolved or reclaimed is a no-op.
type Probe struct {
	Model   string
	org     string
	key     string
	circuit *circuit
	slot    *probeSlot
}

// Transition reports what a call did to one circuit, for logging.
type Transition struct {
	Model string
	// Org is the organization whose request made the call.
	Org      string
	Isolated bool
	From, To State
	// Confirmed is true when the call made Org confirm an open shared
	// circuit, withdrawing the model from it.
	Confirmed bool
	// RecoveryAfter is the open period that just started; zero otherwise.
	RecoveryAfter time.Duration
}

// Changed reports whether the call moved the circuit or an organization's
// view of it.
func (t Transition) Changed() bool { return t.From != t.To || t.Confirmed }

// probeSlot is one holder's claim on a circuit's half-open slot, valid until
// its lease runs out.
type probeSlot struct{ until time.Time }

// resolving holds a slot while its outcome is applied, so a lease that
// expires mid-resolution cannot hand the circuit to a second prober.
var resolving = &probeSlot{until: time.Unix(1<<62, 0)}

type circuit struct {
	// model and org never change, so snapshot readers use them unlocked.
	model string
	// org is set only on isolated circuits.
	org string

	// Guarded by Registry.mu.
	state    State
	failures []time.Time
	// orgFailures holds each organization's recent failures on a shared
	// circuit, newest last, at most OrgConfirmations per organization.
	orgFailures  map[string][]time.Time
	episodeStart time.Time
	openedAt     time.Time
	reopens      int

	// probe is the held half-open slot, nil when none; an open circuit with
	// an unexpired slot is half-open.
	probe atomic.Pointer[probeSlot]
	// lastOffered is the UnixNano of the last claim, so a released probe
	// yields to circuits not yet offered and one unwanted model cannot starve
	// the others.
	lastOffered atomic.Int64
}

// snapshot is what request-path readers see, rebuilt whenever a circuit opens,
// closes, reopens or gains a confirmation.
type snapshot struct {
	withdrawn map[string]map[string]struct{}
	// open lists every open circuit, longest open first.
	open []openCircuit
	// nextDue is when the soonest open circuit becomes claimable.
	nextDue time.Time
}

type openCircuit struct {
	key     string
	circuit *circuit
	dueAt   time.Time
}

// Registry holds every circuit on the replica.
type Registry struct {
	cfg Config
	now func() time.Time

	mu       sync.Mutex
	circuits map[string]*circuit

	published atomic.Pointer[snapshot]
}

// New returns a registry with every circuit closed. now defaults to time.Now.
func New(cfg Config, now func() time.Time) *Registry {
	cfg.Strikes = max(cfg.Strikes, 1)
	cfg.OrgConfirmations = max(cfg.OrgConfirmations, 1)
	if cfg.RecoveryBase <= 0 {
		cfg.RecoveryBase = time.Second
	}
	cfg.RecoveryCap = max(cfg.RecoveryCap, cfg.RecoveryBase)
	if now == nil {
		now = time.Now
	}
	return &Registry{cfg: cfg, now: now, circuits: make(map[string]*circuit)}
}

// Withdrawn returns the models org may not choose automatically: open shared
// circuits it has confirmed, plus its own open isolated circuits. The map is
// shared and must not be modified; nil when empty.
func (r *Registry) Withdrawn(org string) map[string]struct{} {
	if snap := r.published.Load(); snap != nil {
		return snap.withdrawn[org]
	}
	return nil
}

// RecordOverload counts one overload failure of model observed by org from a
// request that does not hold the circuit's probe. isolated selects org's own
// circuit for the model instead of the shared one. A closed circuit opens once
// Strikes failures fall within Window; on an open shared circuit the failure
// can only confirm it for org, since only its probe decides recovery. org must
// be non-empty.
func (r *Registry) RecordOverload(org, model string, isolated bool) Transition {
	if model == "" {
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
	c.state, c.openedAt, c.episodeStart, c.reopens, c.failures = StateOpen, now, now, 0, nil
	transition.To, transition.RecoveryAfter = StateOpen, r.recoveryLocked(0)
	transition.Confirmed = r.confirmedLocked(c, org, now)
	r.publishLocked()
	return transition
}

// ClaimProbe grants org the half-open slot of the first due circuit it may
// probe that eligible accepts, preferring circuits whose probe was offered
// least recently, then the longest open. Any organization may probe a shared
// circuit, so one nobody has confirmed still recovers; an isolated circuit is
// probed only by its owner. A circuit is due once its recovery timeout has
// elapsed and no unexpired probe holds it. Lock-free.
func (r *Registry) ClaimProbe(org string, eligible func(model string) bool) (Probe, Transition, bool) {
	snap := r.published.Load()
	if snap == nil || len(snap.open) == 0 {
		return Probe{}, Transition{}, false
	}
	now := r.now()
	if now.Before(snap.nextDue) {
		return Probe{}, Transition{}, false
	}
	var picked *openCircuit
	var observed *probeSlot
	for i := range snap.open {
		candidate := &snap.open[i]
		c := candidate.circuit
		if now.Before(candidate.dueAt) || (c.org != "" && c.org != org) {
			continue
		}
		slot := c.probe.Load()
		if slot != nil && now.Before(slot.until) {
			continue
		}
		if picked != nil && c.lastOffered.Load() >= picked.circuit.lastOffered.Load() {
			continue
		}
		if !eligible(c.model) {
			continue
		}
		picked, observed = candidate, slot
	}
	if picked == nil {
		return Probe{}, Transition{}, false
	}
	c := picked.circuit
	slot := &probeSlot{until: now.Add(probeLease)}
	if !c.probe.CompareAndSwap(observed, slot) {
		return Probe{}, Transition{}, false
	}
	if r.published.Load() != snap {
		// A resolution republished while this claim was deciding; its due
		// times may be stale, so leave the slot to a request that sees the
		// new snapshot.
		c.probe.CompareAndSwap(slot, observed)
		return Probe{}, Transition{}, false
	}
	c.lastOffered.Store(now.UnixNano())
	probe := Probe{Model: c.model, org: org, key: picked.key, circuit: c, slot: slot}
	return probe, Transition{Model: c.model, Org: org, Isolated: c.org != "", From: StateOpen, To: StateHalfOpen}, true
}

// ReleaseProbe returns a slot whose request ended without resolving it (the
// model was not dispatched, or failed for a reason other than overload). The
// circuit stays open and due, behind any due circuit not yet offered.
// Lock-free; a no-op when the probe was already resolved or reclaimed.
func (r *Registry) ReleaseProbe(p Probe) Transition {
	if p.circuit == nil || !p.circuit.probe.CompareAndSwap(p.slot, nil) {
		return Transition{Model: p.Model}
	}
	return Transition{Model: p.Model, Org: p.org, Isolated: p.circuit.org != "", From: StateHalfOpen, To: StateOpen}
}

// ProbeSucceeded closes the probed circuit and resets its whole state,
// including every organization's confirmation. No-op unless p still holds the
// circuit's half-open slot.
func (r *Registry) ProbeSucceeded(p Probe) Transition {
	return r.resolveProbe(p, true)
}

// ProbeFailed reopens the probed circuit with a doubled recovery timeout,
// capped at RecoveryCap; confirmations carry over. No-op unless p still holds
// the half-open slot.
func (r *Registry) ProbeFailed(p Probe) Transition {
	return r.resolveProbe(p, false)
}

func (r *Registry) resolveProbe(p Probe, closed bool) Transition {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.circuits[p.key]
	if c == nil || c != p.circuit || !c.probe.CompareAndSwap(p.slot, resolving) {
		return Transition{Model: p.Model}
	}
	transition := Transition{Model: p.Model, Org: p.org, Isolated: c.org != "", From: StateHalfOpen, To: StateClosed}
	if closed {
		delete(r.circuits, p.key)
	} else {
		c.openedAt, c.reopens = r.now(), c.reopens+1
		transition.To = StateOpen
		transition.RecoveryAfter = r.recoveryLocked(c.reopens)
	}
	r.publishLocked()
	c.probe.Store(nil)
	return transition
}

// confirmedLocked reports whether c withdraws its model from org: always for
// its owner when isolated, and for a shared circuit only once org has enough
// failures of its own in the current episode. A closed circuit withdraws
// nothing.
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
	recent := func(at time.Time) bool { return at.After(cutoff) }
	for key, c := range r.circuits {
		if c.state != StateClosed || slices.ContainsFunc(c.failures, recent) {
			continue
		}
		stale := true
		for _, failures := range c.orgFailures {
			if slices.ContainsFunc(failures, recent) {
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

func (r *Registry) publishLocked() {
	now := r.now()
	snap := &snapshot{withdrawn: make(map[string]map[string]struct{})}
	withdraw := func(org, model string) {
		if snap.withdrawn[org] == nil {
			snap.withdrawn[org] = make(map[string]struct{})
		}
		snap.withdrawn[org][model] = struct{}{}
	}
	for key, c := range r.circuits {
		if c.state == StateClosed {
			continue
		}
		snap.open = append(snap.open, openCircuit{key: key, circuit: c, dueAt: c.openedAt.Add(r.recoveryLocked(c.reopens))})
		if c.org != "" {
			withdraw(c.org, c.model)
			continue
		}
		for org := range c.orgFailures {
			if r.confirmedLocked(c, org, now) {
				withdraw(org, c.model)
			}
		}
	}
	slices.SortFunc(snap.open, func(a, b openCircuit) int {
		if order := a.circuit.openedAt.Compare(b.circuit.openedAt); order != 0 {
			return order
		}
		return strings.Compare(a.key, b.key)
	})
	if len(snap.open) > 0 {
		snap.nextDue = slices.MinFunc(snap.open, func(a, b openCircuit) int {
			return a.dueAt.Compare(b.dueAt)
		}).dueAt
	}
	r.published.Store(snap)
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
	kept := slices.DeleteFunc(failures, func(at time.Time) bool { return !at.After(cutoff) })
	if len(kept) > limit {
		kept = kept[len(kept)-limit:]
	}
	return kept
}
