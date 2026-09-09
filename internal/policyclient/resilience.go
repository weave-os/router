package policyclient

import (
	"context"
	"sync"
	"time"

	"golang.org/x/sync/semaphore"

	"weave-os/router/internal/router/policy"
)

// ResilienceConfig bounds pressure on one endpoint/contract client. Separate
// clients isolate stable and beta policy state, even when they share a host.
type ResilienceConfig struct {
	MaxConcurrent    int64
	FailureThreshold int
	OpenDuration     time.Duration
	Now              func() time.Time
}

// WithResilience configures admission and circuit recovery before client use.
func WithResilience(config ResilienceConfig) Option {
	return func(c *Client) { c.resilience = newResilience(config) }
}

type resilience struct {
	mu         sync.Mutex
	slots      *semaphore.Weighted
	now        func() time.Time
	threshold  int
	cooldown   time.Duration
	failures   int
	openUntil  time.Time
	probing    bool
	generation uint64
}

func newResilience(config ResilienceConfig) *resilience {
	if config.MaxConcurrent <= 0 {
		config.MaxConcurrent = 64
	}
	if config.FailureThreshold <= 0 {
		config.FailureThreshold = 5
	}
	if config.OpenDuration <= 0 {
		config.OpenDuration = 30 * time.Second
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	return &resilience{slots: semaphore.NewWeighted(config.MaxConcurrent), now: config.Now, threshold: config.FailureThreshold, cooldown: config.OpenDuration}
}

func (r *resilience) admit(ctx context.Context) (func(error), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	probe := !r.openUntil.IsZero()
	if probe && (r.now().Before(r.openUntil) || r.probing) {
		return nil, &policy.DependencyError{Reason: policy.FailureCircuitOpen}
	}
	if !r.slots.TryAcquire(1) {
		return nil, &policy.DependencyError{Reason: policy.FailureOverload}
	}
	if probe {
		r.probing = true
	}
	generation := r.generation
	return func(err error) {
		r.slots.Release(1)
		r.mu.Lock()
		defer r.mu.Unlock()
		if generation != r.generation {
			return
		}
		r.probing = false
		if err == nil {
			r.failures = 0
			r.openUntil = time.Time{}
			if probe {
				r.generation++
			}
			return
		}
		reason := policy.FailureReasonFor(err)
		var endpointFailure bool
		switch reason {
		case policy.FailureTransport, policy.FailureTimeout, policy.FailureOverload, policy.FailureAuth, policy.FailureContract, policy.FailureCanceled:
			endpointFailure = true
		}
		if !endpointFailure {
			if probe && reason != policy.FailureCanceled {
				r.failures = 0
				r.openUntil = time.Time{}
				r.generation++
			}
			return
		}
		r.failures++
		if probe || r.failures >= r.threshold {
			r.openUntil = r.now().Add(r.cooldown)
			r.generation++
		}
	}, nil
}

func (c *Client) decisionContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return policy.DecisionContext(ctx, time.Now(), c.timeout)
}

func (r *resilience) ready() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.openUntil.IsZero()
}

func (r *resilience) probeEligible() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return !r.openUntil.IsZero() && !r.now().Before(r.openUntil) && !r.probing
}
