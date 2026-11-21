package dispatch

import (
	"context"
	"errors"
	"maps"
	"time"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/breaker"
)

const (
	ProviderOverloadedStatus        = 529
	OverloadBreakerReason           = "provider_overloaded"
	DefaultRateLimitCooldownSeconds = 45
)

type Throttling struct {
	Overload  OverloadBreaker
	RateLimit RateLimitPolicy
}

type RateLimitPolicy struct {
	transient       bool
	cooldownSeconds int
}

func NewRateLimitPolicy(transient bool, cooldownSeconds int) RateLimitPolicy {
	return RateLimitPolicy{transient: transient, cooldownSeconds: cooldownSeconds}
}

func (p RateLimitPolicy) Transient(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyTransientRateLimit, p.transient)
}

func (p RateLimitPolicy) Cooldown(ctx context.Context) time.Duration {
	seconds := flags.IntOr(ctx, flags.KeyRateLimitCooldownSeconds, p.cooldownSeconds)
	if seconds < 1 {
		seconds = DefaultRateLimitCooldownSeconds
	}
	return time.Duration(seconds) * time.Second
}

type overloadProbeContextKey struct{}

type OverloadBreaker struct {
	enabled  bool
	registry *breaker.Registry
}

func NewOverloadBreaker(enabled bool, registry *breaker.Registry) OverloadBreaker {
	return OverloadBreaker{enabled: enabled, registry: registry}
}

func (b OverloadBreaker) Enabled(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyProviderOverloaded, b.enabled)
}

func (b OverloadBreaker) Armed(ctx context.Context) bool {
	return b.registry != nil && b.Enabled(ctx)
}

func (b OverloadBreaker) OpenModels(ctx context.Context, org string) map[string]struct{} {
	if b.registry == nil || org == "" || !b.Enabled(ctx) {
		return nil
	}
	withdrawn := b.registry.Withdrawn(org)
	probe, held := ctx.Value(overloadProbeContextKey{}).(breaker.Probe)
	if _, excluded := withdrawn[probe.Model]; !held || !excluded {
		return withdrawn
	}
	readmitted := maps.Clone(withdrawn)
	delete(readmitted, probe.Model)
	return readmitted
}

func (b OverloadBreaker) ClaimProbe(ctx context.Context, org string, eligible func(model string) bool) (breaker.Probe, func(), bool) {
	probe, transition, ok := b.registry.ClaimProbe(org, eligible)
	if !ok {
		return breaker.Probe{}, nil, false
	}
	logOverloadTransition(ctx, transition)
	release := func() { b.registry.ReleaseProbe(probe) }
	return probe, release, true
}

func (b OverloadBreaker) ReleaseProbe(probe breaker.Probe) {
	b.registry.ReleaseProbe(probe)
}

func WithOverloadProbe(ctx context.Context, probe breaker.Probe) context.Context {
	return context.WithValue(ctx, overloadProbeContextKey{}, probe)
}

func (b OverloadBreaker) RecordOutcome(ctx context.Context, org, model, provider string, committed bool, err error) {
	if b.registry == nil || org == "" || !b.Enabled(ctx) {
		return
	}
	overloaded := err != nil && !committed && !errors.Is(err, context.Canceled) && IsProviderOverloaded(err)
	if probe, held := ctx.Value(overloadProbeContextKey{}).(breaker.Probe); held && probe.Model == model {
		switch {
		case err == nil:
			logOverloadTransition(ctx, b.registry.ProbeSucceeded(probe))
		case overloaded:
			logOverloadTransition(ctx, b.registry.ProbeFailed(probe))
		}
		return
	}
	if overloaded {
		logOverloadTransition(ctx, b.registry.RecordOverload(org, model, providers.IsGateway(provider)))
	}
}

func IsProviderOverloaded(err error) bool {
	var buffered *providers.UpstreamErrorResponse
	return errors.As(err, &buffered) && buffered.Status == ProviderOverloadedStatus
}

func logOverloadTransition(ctx context.Context, transition breaker.Transition) {
	if !transition.Changed() {
		return
	}
	log := observability.FromContext(ctx).Info
	if transition.To == breaker.StateHalfOpen {
		log = observability.FromContext(ctx).Debug
	}
	log("Overload circuit changed state",
		"model", transition.Model,
		"installation_id", transition.Org,
		"isolated", transition.Isolated,
		"from", string(transition.From),
		"to", string(transition.To),
		"org_confirmed", transition.Confirmed,
		"recovery_after_ms", transition.RecoveryAfter.Milliseconds(),
	)
}
