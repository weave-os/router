package breaker

import (
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type State string

const (
	StateClosed   State = "closed"
	StateOpen     State = "open"
	StateHalfOpen State = "half_open"
)

type Config struct {
	Strikes          int
	Window           time.Duration
	OrgConfirmations int
	RecoveryBase     time.Duration
	RecoveryCap      time.Duration
}

type Probe struct {
	Model   string
	org     string
	key     string
	circuit *circuit
	slot    *probeSlot
}

type Transition struct {
	Model         string
	Org           string
	Isolated      bool
	From, To      State
	Confirmed     bool
	RecoveryAfter time.Duration
}

func (t Transition) Changed() bool { return t.From != t.To || t.Confirmed }

type probeSlot struct{ until time.Time }

var resolving = &probeSlot{until: time.Unix(1<<62, 0)}

type circuit struct {
	model string
	org   string

	state        State
	failures     []time.Time
	orgFailures  map[string][]time.Time
	episodeStart time.Time
	openedAt     time.Time
	reopens      int

	probe       atomic.Pointer[probeSlot]
	lastOffered atomic.Int64
}

type snapshot struct {
	withdrawn map[string]map[string]struct{}
	open      []openCircuit
	nextDue   time.Time
}

type openCircuit struct {
	key     string
	circuit *circuit
	dueAt   time.Time
}

type Registry struct {
	cfg Config
	now func() time.Time

	mu       sync.Mutex
	circuits map[string]*circuit

	published atomic.Pointer[snapshot]
}

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

func (r *Registry) Withdrawn(org string) map[string]struct{} {
	if snap := r.published.Load(); snap != nil {
		return snap.withdrawn[org]
	}
	return nil
}

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
		c.probe.CompareAndSwap(slot, observed)
		return Probe{}, Transition{}, false
	}
	c.lastOffered.Store(now.UnixNano())
	probe := Probe{Model: c.model, org: org, key: picked.key, circuit: c, slot: slot}
	return probe, Transition{Model: c.model, Org: org, Isolated: c.org != "", From: StateOpen, To: StateHalfOpen}, true
}

func (r *Registry) ReleaseProbe(p Probe) Transition {
	if p.circuit == nil || !p.circuit.probe.CompareAndSwap(p.slot, nil) {
		return Transition{Model: p.Model}
	}
	return Transition{Model: p.Model, Org: p.org, Isolated: p.circuit.org != "", From: StateHalfOpen, To: StateOpen}
}

func (r *Registry) ProbeSucceeded(p Probe) Transition {
	return r.resolveProbe(p, true)
}

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

func (r *Registry) cutoffLocked(c *circuit, now time.Time) time.Time {
	if c.state == StateClosed {
		return now.Add(-r.cfg.Window)
	}
	return c.episodeStart.Add(-r.cfg.Window)
}

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

func recentFailures(failures []time.Time, cutoff time.Time, limit int) []time.Time {
	kept := slices.DeleteFunc(failures, func(at time.Time) bool { return !at.After(cutoff) })
	if len(kept) > limit {
		kept = kept[len(kept)-limit:]
	}
	return kept
}
