package breaker_test

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router/breaker"
)

const (
	overloadedModel = "claude-opus-5"
	otherModel      = "gpt-5.6-luna"
)

type clock struct{ now time.Time }

func (c *clock) Now() time.Time          { return c.now }
func (c *clock) Advance(d time.Duration) { c.now = c.now.Add(d) }

func newClock() *clock { return &clock{now: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)} }

func anyModel(string) bool { return true }

func isOpen(r *breaker.Registry, model string) bool {
	_, ok := r.OpenModels()[model]
	return ok
}

func newRegistry(c *clock) *breaker.Registry {
	return breaker.New(breaker.Config{
		Strikes:      3,
		Window:       2 * time.Minute,
		RecoveryBase: 30 * time.Second,
		RecoveryCap:  2 * time.Minute,
		ProbeLease:   5 * time.Minute,
	}, c.Now)
}

func openCircuit(t *testing.T, r *breaker.Registry, model string) {
	t.Helper()
	for range 3 {
		r.RecordOverload(model)
	}
	require.Equal(t, breaker.StateOpen, r.State(model))
}

func claim(t *testing.T, r *breaker.Registry) breaker.Probe {
	t.Helper()
	probe, _, ok := r.ClaimProbe(anyModel)
	require.True(t, ok)
	return probe
}

func TestOpensAfterStrikesWithinWindow(t *testing.T) {
	c := newClock()
	r := newRegistry(c)

	r.RecordOverload(overloadedModel)
	r.RecordOverload(overloadedModel)
	assert.Nil(t, r.OpenModels(), "two strikes stay below the threshold")

	opened := r.RecordOverload(overloadedModel)

	assert.Equal(t, breaker.StateOpen, opened.To)
	assert.Equal(t, 3, opened.Failures)
	assert.Equal(t, 30*time.Second, opened.RecoveryAfter)
	assert.True(t, isOpen(r, overloadedModel))
}

func TestStrikesOutsideWindowAgeOut(t *testing.T) {
	c := newClock()
	r := newRegistry(c)

	r.RecordOverload(overloadedModel)
	r.RecordOverload(overloadedModel)
	c.Advance(2*time.Minute + time.Second)
	r.RecordOverload(overloadedModel)

	assert.Equal(t, breaker.StateClosed, r.State(overloadedModel))
	assert.Nil(t, r.OpenModels())
}

func TestOpenCircuitIsNotDueBeforeRecoveryTimeout(t *testing.T) {
	c := newClock()
	r := newRegistry(c)
	openCircuit(t, r, overloadedModel)

	c.Advance(29 * time.Second)
	_, _, ok := r.ClaimProbe(anyModel)

	assert.False(t, ok)
	assert.True(t, isOpen(r, overloadedModel))
}

func TestHalfOpenAdmitsExactlyOneProbe(t *testing.T) {
	c := newClock()
	r := newRegistry(c)
	openCircuit(t, r, overloadedModel)
	c.Advance(30 * time.Second)

	var wg sync.WaitGroup
	var mu sync.Mutex
	claims := 0
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, ok := r.ClaimProbe(anyModel); ok {
				mu.Lock()
				claims++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, 1, claims)
	assert.Equal(t, breaker.StateHalfOpen, r.State(overloadedModel))
	assert.True(t, isOpen(r, overloadedModel), "half-open stays excluded for everyone but the prober")
}

func TestServedProbeClosesAndResetsState(t *testing.T) {
	c := newClock()
	r := newRegistry(c)
	openCircuit(t, r, overloadedModel)
	c.Advance(30 * time.Second)

	closed := r.ProbeSucceeded(claim(t, r))

	assert.Equal(t, breaker.StateClosed, closed.To)
	assert.Nil(t, r.OpenModels())
	r.RecordOverload(overloadedModel)
	r.RecordOverload(overloadedModel)
	assert.Equal(t, breaker.StateClosed, r.State(overloadedModel), "strikes from before the outage do not carry over")
}

func TestFailedProbeReopensWithBackoffUpToCap(t *testing.T) {
	c := newClock()
	r := newRegistry(c)
	openCircuit(t, r, overloadedModel)

	for _, want := range []time.Duration{time.Minute, 2 * time.Minute, 2 * time.Minute} {
		c.Advance(5 * time.Minute)

		reopened := r.ProbeFailed(claim(t, r))

		assert.Equal(t, breaker.StateOpen, reopened.To)
		assert.Equal(t, want, reopened.RecoveryAfter)
	}
	c.Advance(2*time.Minute - time.Second)
	_, _, ok := r.ClaimProbe(anyModel)
	assert.False(t, ok, "the capped recovery timeout still applies")
}

// Only the probe decides recovery: a 529 from a request that began before the
// circuit opened neither reopens it early nor counts against the probe.
func TestOverloadWhileHalfOpenLeavesProbeInCharge(t *testing.T) {
	c := newClock()
	r := newRegistry(c)
	openCircuit(t, r, overloadedModel)
	c.Advance(30 * time.Second)
	probe := claim(t, r)

	assert.False(t, r.RecordOverload(overloadedModel).Changed())
	assert.Equal(t, breaker.StateHalfOpen, r.State(overloadedModel))
	assert.Equal(t, breaker.StateClosed, r.ProbeSucceeded(probe).To)
}

func TestReleasedProbeIsDueAgain(t *testing.T) {
	c := newClock()
	r := newRegistry(c)
	openCircuit(t, r, overloadedModel)
	c.Advance(30 * time.Second)

	released := r.ReleaseProbe(claim(t, r))
	again := claim(t, r)

	assert.Equal(t, breaker.StateOpen, released.To)
	assert.Equal(t, overloadedModel, again.Model)
}

// A request whose probe was released keeps a model from being reclaimed
// first forever: the next claim goes to the due circuit not yet offered.
func TestReleasedProbeYieldsToUnofferedCircuit(t *testing.T) {
	c := newClock()
	r := newRegistry(c)
	openCircuit(t, r, overloadedModel)
	c.Advance(time.Second)
	openCircuit(t, r, otherModel)
	c.Advance(30 * time.Second)

	first := claim(t, r)
	require.Equal(t, overloadedModel, first.Model, "the longest-open circuit is offered first")
	r.ReleaseProbe(first)
	second := claim(t, r)

	assert.Equal(t, otherModel, second.Model)
}

func TestStaleProbeCannotResolveCircuit(t *testing.T) {
	c := newClock()
	r := newRegistry(c)
	openCircuit(t, r, overloadedModel)
	c.Advance(30 * time.Second)
	stale := claim(t, r)
	r.ProbeFailed(stale)
	c.Advance(time.Minute)
	current := claim(t, r)

	assert.False(t, r.ProbeSucceeded(stale).Changed(), "a resolved probe cannot close the circuit")
	assert.False(t, r.ReleaseProbe(stale).Changed())
	assert.Equal(t, breaker.StateHalfOpen, r.State(overloadedModel))
	assert.Equal(t, breaker.StateClosed, r.ProbeSucceeded(current).To)
}

func TestAbandonedProbeIsReclaimedAfterLease(t *testing.T) {
	c := newClock()
	r := newRegistry(c)
	openCircuit(t, r, overloadedModel)
	c.Advance(30 * time.Second)
	stale := claim(t, r)

	c.Advance(4 * time.Minute)
	_, _, early := r.ClaimProbe(anyModel)
	c.Advance(time.Minute)
	_, _, late := r.ClaimProbe(anyModel)

	assert.False(t, early)
	assert.True(t, late)
	assert.False(t, r.ProbeSucceeded(stale).Changed(), "the abandoned holder cannot resolve the new probe")
	assert.Equal(t, breaker.StateHalfOpen, r.State(overloadedModel))
}

func TestClaimSkipsIneligibleModels(t *testing.T) {
	c := newClock()
	r := newRegistry(c)
	openCircuit(t, r, overloadedModel)
	c.Advance(30 * time.Second)

	_, _, ok := r.ClaimProbe(func(string) bool { return false })

	assert.False(t, ok)
	assert.Equal(t, breaker.StateOpen, r.State(overloadedModel), "an ineligible request leaves the circuit due for the next one")
}
