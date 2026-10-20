package breaker_test

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router/breaker"
)

const overloadedModel = "claude-opus-5"

type clock struct{ now time.Time }

func (c *clock) Now() time.Time                 { return c.now }
func (c *clock) Advance(d time.Duration)        { c.now = c.now.Add(d) }
func newClock() *clock                          { return &clock{now: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)} }
func anyModel(string) bool                      { return true }
func isOpen(r *breaker.Registry, m string) bool { _, ok := r.OpenModels()[m]; return ok }

func newRegistry(c *clock) *breaker.Registry {
	return breaker.New(breaker.Config{
		Strikes:      3,
		Window:       2 * time.Minute,
		RecoveryBase: 30 * time.Second,
		RecoveryCap:  2 * time.Minute,
		ProbeLease:   5 * time.Minute,
	}, c.Now)
}

func openCircuit(t *testing.T, r *breaker.Registry) {
	t.Helper()
	for range 3 {
		r.RecordOverload(overloadedModel)
	}
	require.Equal(t, breaker.StateOpen, r.State(overloadedModel))
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

func TestServedOnClosedCircuitKeepsStrikes(t *testing.T) {
	c := newClock()
	r := newRegistry(c)

	r.RecordOverload(overloadedModel)
	r.RecordOverload(overloadedModel)
	r.RecordServed(overloadedModel)
	r.RecordOverload(overloadedModel)

	assert.Equal(t, breaker.StateOpen, r.State(overloadedModel), "a served request between overloads does not reset the window")
}

func TestOpenCircuitIsNotDueBeforeRecoveryTimeout(t *testing.T) {
	c := newClock()
	r := newRegistry(c)
	openCircuit(t, r)

	c.Advance(29 * time.Second)
	_, _, ok := r.ClaimProbe(anyModel)

	assert.False(t, ok)
	assert.True(t, isOpen(r, overloadedModel))
}

func TestHalfOpenAdmitsExactlyOneProbe(t *testing.T) {
	c := newClock()
	r := newRegistry(c)
	openCircuit(t, r)
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
	openCircuit(t, r)
	c.Advance(30 * time.Second)
	_, _, ok := r.ClaimProbe(anyModel)
	require.True(t, ok)

	closed := r.RecordServed(overloadedModel)

	assert.Equal(t, breaker.StateClosed, closed.To)
	assert.Nil(t, r.OpenModels())
	r.RecordOverload(overloadedModel)
	r.RecordOverload(overloadedModel)
	assert.Equal(t, breaker.StateClosed, r.State(overloadedModel), "strikes from before the outage do not carry over")
}

func TestFailedProbeReopensWithBackoffUpToCap(t *testing.T) {
	c := newClock()
	r := newRegistry(c)
	openCircuit(t, r)

	for _, want := range []time.Duration{time.Minute, 2 * time.Minute, 2 * time.Minute} {
		c.Advance(5 * time.Minute)
		_, _, ok := r.ClaimProbe(anyModel)
		require.True(t, ok)

		reopened := r.RecordOverload(overloadedModel)

		assert.Equal(t, breaker.StateOpen, reopened.To)
		assert.Equal(t, want, reopened.RecoveryAfter)
	}
	c.Advance(2*time.Minute - time.Second)
	_, _, ok := r.ClaimProbe(anyModel)
	assert.False(t, ok, "the capped recovery timeout still applies")
}

func TestReleasedProbeIsImmediatelyDueAgain(t *testing.T) {
	c := newClock()
	r := newRegistry(c)
	openCircuit(t, r)
	c.Advance(30 * time.Second)
	probe, _, ok := r.ClaimProbe(anyModel)
	require.True(t, ok)

	released := r.ReleaseProbe(probe)
	again, _, reclaimed := r.ClaimProbe(anyModel)

	assert.Equal(t, breaker.StateOpen, released.To)
	assert.True(t, reclaimed)
	assert.Equal(t, overloadedModel, again.Model)
}

func TestReleaseAfterResolutionIsNoOp(t *testing.T) {
	c := newClock()
	r := newRegistry(c)
	openCircuit(t, r)
	c.Advance(30 * time.Second)
	probe, _, ok := r.ClaimProbe(anyModel)
	require.True(t, ok)
	r.RecordServed(overloadedModel)

	assert.False(t, r.ReleaseProbe(probe).Changed())
	assert.Equal(t, breaker.StateClosed, r.State(overloadedModel))
}

func TestAbandonedProbeIsReclaimedAfterLease(t *testing.T) {
	c := newClock()
	r := newRegistry(c)
	openCircuit(t, r)
	c.Advance(30 * time.Second)
	stale, _, ok := r.ClaimProbe(anyModel)
	require.True(t, ok)

	c.Advance(4 * time.Minute)
	_, _, early := r.ClaimProbe(anyModel)
	c.Advance(time.Minute)
	_, _, late := r.ClaimProbe(anyModel)

	assert.False(t, early)
	assert.True(t, late)
	assert.False(t, r.ReleaseProbe(stale).Changed(), "the abandoned holder cannot release the new probe")
	assert.Equal(t, breaker.StateHalfOpen, r.State(overloadedModel))
}

func TestClaimSkipsIneligibleModels(t *testing.T) {
	c := newClock()
	r := newRegistry(c)
	openCircuit(t, r)
	c.Advance(30 * time.Second)

	_, _, ok := r.ClaimProbe(func(string) bool { return false })

	assert.False(t, ok)
	assert.Equal(t, breaker.StateOpen, r.State(overloadedModel), "an ineligible request leaves the circuit due for the next one")
}
