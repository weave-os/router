package sessionpin

import (
	"context"
	"time"

	"github.com/google/uuid"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/router"
)

type FullStore interface {
	Store
	CooldownStore
	TurnStrikeStore
	RecoveryProbeStore
	TokenizerCalibrationStore
}

type flagGatedStore struct {
	base FullStore
}

func GatedByFlag(base FullStore) FullStore {
	return flagGatedStore{base: base}
}

func Enabled(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeySessionPinEnabled, true)
}

func (g flagGatedStore) Get(ctx context.Context, sessionKey [SessionKeyLen]byte, role string) (Pin, bool, error) {
	if !Enabled(ctx) {
		return Pin{}, false, nil
	}
	return g.base.Get(ctx, sessionKey, role)
}

func (g flagGatedStore) Consume(ctx context.Context, sessionKey [SessionKeyLen]byte, role string, expectedStrategy router.Strategy) (Pin, bool, error) {
	if !Enabled(ctx) {
		return Pin{}, false, nil
	}
	return g.base.Consume(ctx, sessionKey, role, expectedStrategy)
}

func (g flagGatedStore) Upsert(ctx context.Context, p Pin) error {
	if !Enabled(ctx) {
		return nil
	}
	return g.base.Upsert(ctx, p)
}

func (g flagGatedStore) UpdateUsage(ctx context.Context, sessionKey [SessionKeyLen]byte, role string, usage Usage) error {
	if !Enabled(ctx) {
		return nil
	}
	return g.base.UpdateUsage(ctx, sessionKey, role, usage)
}

func (g flagGatedStore) IncrementUpstreamErrors(ctx context.Context, sessionKey [SessionKeyLen]byte, role string, expectedStrategy router.Strategy) (int, error) {
	if !Enabled(ctx) {
		return 0, nil
	}
	return g.base.IncrementUpstreamErrors(ctx, sessionKey, role, expectedStrategy)
}

func (g flagGatedStore) ResetUpstreamErrors(ctx context.Context, sessionKey [SessionKeyLen]byte, role string, expectedStrategy router.Strategy) error {
	if !Enabled(ctx) {
		return nil
	}
	return g.base.ResetUpstreamErrors(ctx, sessionKey, role, expectedStrategy)
}

func (g flagGatedStore) IncrementOverloadErrors(ctx context.Context, sessionKey [SessionKeyLen]byte, role string, expectedStrategy router.Strategy) (int, error) {
	if !Enabled(ctx) {
		return 0, nil
	}
	return g.base.IncrementOverloadErrors(ctx, sessionKey, role, expectedStrategy)
}

func (g flagGatedStore) ResetOverloadErrors(ctx context.Context, sessionKey [SessionKeyLen]byte, role string, expectedStrategy router.Strategy) error {
	if !Enabled(ctx) {
		return nil
	}
	return g.base.ResetOverloadErrors(ctx, sessionKey, role, expectedStrategy)
}

func (g flagGatedStore) DisableProvider(ctx context.Context, sessionKey [SessionKeyLen]byte, role, provider string, expectedStrategy router.Strategy) error {
	if !Enabled(ctx) {
		return nil
	}
	return g.base.DisableProvider(ctx, sessionKey, role, provider, expectedStrategy)
}

func (g flagGatedStore) ExpireAndDemoteModel(ctx context.Context, expired Pin, model string, reason DemotionReason) error {
	if !Enabled(ctx) {
		return nil
	}
	return g.base.ExpireAndDemoteModel(ctx, expired, model, reason)
}

func (g flagGatedStore) SweepExpired(ctx context.Context) error {
	return g.base.SweepExpired(ctx)
}

func (g flagGatedStore) ExpireAndCoolDownModel(ctx context.Context, expired Pin, model string, until time.Time, reason DemotionReason) error {
	if !Enabled(ctx) {
		return nil
	}
	return g.base.ExpireAndCoolDownModel(ctx, expired, model, until, reason)
}

func (g flagGatedStore) StrikeTurnModel(ctx context.Context, sessionKey [SessionKeyLen]byte, role, model string) error {
	if !Enabled(ctx) {
		return nil
	}
	return g.base.StrikeTurnModel(ctx, sessionKey, role, model)
}

func (g flagGatedStore) ClearTurnStrikes(ctx context.Context, sessionKey [SessionKeyLen]byte, role string) error {
	if !Enabled(ctx) {
		return nil
	}
	return g.base.ClearTurnStrikes(ctx, sessionKey, role)
}

func (g flagGatedStore) AcquireRecoveryProbe(ctx context.Context, sessionKey [SessionKeyLen]byte, model string, token uuid.UUID, leaseFor time.Duration) (bool, error) {
	if !Enabled(ctx) {
		return false, nil
	}
	return g.base.AcquireRecoveryProbe(ctx, sessionKey, model, token, leaseFor)
}

func (g flagGatedStore) ReleaseRecoveryProbe(ctx context.Context, sessionKey [SessionKeyLen]byte, model string, token uuid.UUID) error {
	if !Enabled(ctx) {
		return nil
	}
	return g.base.ReleaseRecoveryProbe(ctx, sessionKey, model, token)
}

func (g flagGatedStore) ClearDemotionCooldown(ctx context.Context, sessionKey [SessionKeyLen]byte, role, model string, observedUntil time.Time) error {
	if !Enabled(ctx) {
		return nil
	}
	return g.base.ClearDemotionCooldown(ctx, sessionKey, role, model, observedUntil)
}

func (g flagGatedStore) UpdateTokenizerCalibration(ctx context.Context, sessionKey [SessionKeyLen]byte, role string, expectedStrategy router.Strategy, cal TokenizerCalibration) error {
	if !Enabled(ctx) {
		return nil
	}
	return g.base.UpdateTokenizerCalibration(ctx, sessionKey, role, expectedStrategy, cal)
}
