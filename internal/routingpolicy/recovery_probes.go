package routingpolicy

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router/sessionpin"
)

const RecoveryProbeLeaseDuration = 15 * time.Minute

type localRecoveryProbeLease struct {
	id    uint64
	until time.Time
}

type RecoveryProbeLeases struct {
	mu     sync.Mutex
	leases map[string]localRecoveryProbeLease
	nextID uint64
}

func RecoveryProbeLeaseFor(ctx context.Context, now time.Time) time.Duration {
	leaseFor := RecoveryProbeLeaseDuration
	if deadline, hasDeadline := ctx.Deadline(); hasDeadline {
		if untilDeadline := deadline.Add(time.Minute).Sub(now); untilDeadline > leaseFor {
			leaseFor = untilDeadline
		}
	}
	return leaseFor
}

func (l *RecoveryProbeLeases) Acquire(ctx context.Context, store sessionpin.Store, now func() time.Time, sessionKey [sessionpin.SessionKeyLen]byte, model string) (func(), bool) {
	if sessionKey == ([sessionpin.SessionKeyLen]byte{}) || model == "" {
		return func() {}, true
	}
	leaseFor := RecoveryProbeLeaseFor(ctx, now())
	if durable, ok := store.(sessionpin.RecoveryProbeStore); ok {
		return acquireDurableRecoveryProbe(ctx, durable, sessionKey, model, leaseFor)
	}
	key := string(sessionKey[:]) + "\x00" + model
	acquiredAt := now()
	l.mu.Lock()
	if lease, exists := l.leases[key]; exists && acquiredAt.Before(lease.until) {
		l.mu.Unlock()
		return nil, false
	}
	if l.leases == nil {
		l.leases = make(map[string]localRecoveryProbeLease)
	}
	l.nextID++
	leaseID := l.nextID
	l.leases[key] = localRecoveryProbeLease{id: leaseID, until: acquiredAt.Add(leaseFor)}
	l.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			if l.leases[key].id == leaseID {
				delete(l.leases, key)
			}
			l.mu.Unlock()
		})
	}, true
}

func acquireDurableRecoveryProbe(ctx context.Context, store sessionpin.RecoveryProbeStore, sessionKey [sessionpin.SessionKeyLen]byte, model string, leaseFor time.Duration) (func(), bool) {
	token := uuid.New()
	acquired, err := store.AcquireRecoveryProbe(ctx, sessionKey, model, token, leaseFor)
	if err != nil {
		observability.FromContext(ctx).Warn("failed to acquire recovery probe lease; keeping model out for this request", "model", model, "err", err)
		return nil, false
	}
	if !acquired {
		return nil, false
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
			defer cancel()
			if err := store.ReleaseRecoveryProbe(releaseCtx, sessionKey, model, token); err != nil {
				observability.FromContext(ctx).Warn("failed to release recovery probe lease; it will expire", "model", model, "err", err)
			}
		})
	}, true
}

func ClearRecoveredCooldown(ctx context.Context, store sessionpin.Store, sessionKey [sessionpin.SessionKeyLen]byte, roles []string, servedModel string, observedUntil time.Time) {
	durable, ok := store.(sessionpin.RecoveryProbeStore)
	if !ok {
		return
	}
	for _, role := range roles {
		if err := durable.ClearDemotionCooldown(context.WithoutCancel(ctx), sessionKey, role, servedModel, observedUntil); err != nil {
			observability.FromContext(ctx).Warn("failed to clear recovered cooldown; next turns keep probing", "model", servedModel, "role", role, "err", err)
		}
	}
}
