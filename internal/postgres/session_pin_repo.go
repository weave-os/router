package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/sqlc"

	"github.com/jackc/pgx/v5/pgtype"
)

// SessionPinRepo adapts sessionpin.Store to the SQLC-generated queries.
type SessionPinRepo struct {
	tx sqlc.DBTX
}

// NewSessionPinRepo wires the adapter over a pgx pool or transaction.
func NewSessionPinRepo(tx sqlc.DBTX) *SessionPinRepo {
	return &SessionPinRepo{tx: tx}
}

var _ sessionpin.Store = (*SessionPinRepo)(nil)

func (r *SessionPinRepo) Get(ctx context.Context, sessionKey [sessionpin.SessionKeyLen]byte, role string) (sessionpin.Pin, bool, error) {
	q := sqlc.New(r.tx)
	row, err := q.GetSessionPin(ctx, sqlc.GetSessionPinParams{
		SessionKey: sessionKey[:],
		Role:       role,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return sessionpin.Pin{}, false, nil
		}
		return sessionpin.Pin{}, false, err
	}
	return toSessionPin(row), true, nil
}

// Consume atomically removes and returns an unexpired one-shot pin.
func (r *SessionPinRepo) Consume(ctx context.Context, sessionKey [sessionpin.SessionKeyLen]byte, role string, expectedStrategy router.Strategy) (sessionpin.Pin, bool, error) {
	q := sqlc.New(r.tx)
	row, err := q.DeleteSessionPin(ctx, sqlc.DeleteSessionPinParams{
		SessionKey:              sessionKey[:],
		Role:                    role,
		ExpectedRoutingStrategy: string(expectedStrategy),
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return sessionpin.Pin{}, false, nil
		}
		return sessionpin.Pin{}, false, err
	}
	return toSessionPin(row), true, nil
}

func (r *SessionPinRepo) Upsert(ctx context.Context, p sessionpin.Pin) error {
	q := sqlc.New(r.tx)
	return q.UpsertSessionPin(ctx, sqlc.UpsertSessionPinParams{
		SessionKey:      p.SessionKey[:],
		Role:            p.Role,
		InstallationID:  p.InstallationID,
		PinnedProvider:  p.Provider,
		PinnedModel:     p.Model,
		PinnedEffort:    p.Effort,
		PairedProvider:  p.PairedProvider,
		PairedModel:     p.PairedModel,
		DecisionReason:  p.Reason,
		RoutingStrategy: string(p.Strategy),
		PolicyGroup:     p.PolicyGroup,
		TurnCount:       int32(p.TurnCount),
		PinnedUntil:     pgtype.Timestamp{Time: p.PinnedUntil.UTC(), Valid: true},
	})
}

// UpdateUsage records the previous turn's usage on the pin row. A missing
// pin (evicted/swept/never created) is a no-op, not an error. A zero
// EndedAt is stamped with time.Now so the column is always populated.
func (r *SessionPinRepo) UpdateUsage(ctx context.Context, sessionKey [sessionpin.SessionKeyLen]byte, role string, usage sessionpin.Usage) error {
	endedAt := usage.EndedAt
	if endedAt.IsZero() {
		endedAt = time.Now()
	}
	q := sqlc.New(r.tx)
	return q.UpdateSessionPinUsage(ctx, sqlc.UpdateSessionPinUsageParams{
		SessionKey:              sessionKey[:],
		Role:                    role,
		LastInputTokens:         int32(usage.InputTokens),
		LastCachedReadTokens:    int32(usage.CachedReadTokens),
		LastCachedWriteTokens:   int32(usage.CachedWriteTokens),
		LastOutputTokens:        int32(usage.OutputTokens),
		LastTurnEndedAt:         pgtype.Timestamptz{Time: endedAt.UTC(), Valid: true},
		LastServedModel:         usage.ServedModel,
		LastServedProvider:      usage.ServedProvider,
		PriorServedModel:        usage.PriorServedModel,
		SessionEverSwitched:     usage.SessionEverSwitched,
		ExpectedRoutingStrategy: string(usage.Strategy),
	})
}

// IncrementUpstreamErrors atomically bumps the consecutive-error counter.
// A missing pin (already evicted or never created) returns (0, nil): the
// two-strike check treats it as a no-op since there's no row left to evict.
func (r *SessionPinRepo) IncrementUpstreamErrors(ctx context.Context, sessionKey [sessionpin.SessionKeyLen]byte, role string, expectedStrategy router.Strategy) (int, error) {
	q := sqlc.New(r.tx)
	count, err := q.IncrementSessionPinUpstreamErrors(ctx, sqlc.IncrementSessionPinUpstreamErrorsParams{
		SessionKey:              sessionKey[:],
		Role:                    role,
		ExpectedRoutingStrategy: string(expectedStrategy),
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, err
	}
	return int(count), nil
}

// ResetUpstreamErrors clears the consecutive-error counter after a
// successful turn. Missing pin is a no-op, same as UpdateUsage.
func (r *SessionPinRepo) ResetUpstreamErrors(ctx context.Context, sessionKey [sessionpin.SessionKeyLen]byte, role string, expectedStrategy router.Strategy) error {
	q := sqlc.New(r.tx)
	return q.ResetSessionPinUpstreamErrors(ctx, sqlc.ResetSessionPinUpstreamErrorsParams{
		SessionKey:              sessionKey[:],
		Role:                    role,
		ExpectedRoutingStrategy: string(expectedStrategy),
	})
}

// IncrementOverloadErrors atomically bumps the consecutive-529-exhaustion
// counter. A missing pin returns (0, nil), mirroring IncrementUpstreamErrors.
func (r *SessionPinRepo) IncrementOverloadErrors(ctx context.Context, sessionKey [sessionpin.SessionKeyLen]byte, role string, expectedStrategy router.Strategy) (int, error) {
	q := sqlc.New(r.tx)
	count, err := q.IncrementSessionPinOverloadErrors(ctx, sqlc.IncrementSessionPinOverloadErrorsParams{
		SessionKey:              sessionKey[:],
		Role:                    role,
		ExpectedRoutingStrategy: string(expectedStrategy),
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, err
	}
	return int(count), nil
}

// ResetOverloadErrors clears the consecutive-529-exhaustion counter after a
// successful turn. Missing pin is a no-op, same as ResetUpstreamErrors.
func (r *SessionPinRepo) ResetOverloadErrors(ctx context.Context, sessionKey [sessionpin.SessionKeyLen]byte, role string, expectedStrategy router.Strategy) error {
	q := sqlc.New(r.tx)
	return q.ResetSessionPinOverloadErrors(ctx, sqlc.ResetSessionPinOverloadErrorsParams{
		SessionKey:              sessionKey[:],
		Role:                    role,
		ExpectedRoutingStrategy: string(expectedStrategy),
	})
}

// DisableProvider appends provider to disabled_providers (deduped) and
// resets the overload strike counter in the same write. Missing pin is a
// no-op: the eviction that accompanies this call has nothing left to guard.
func (r *SessionPinRepo) DisableProvider(ctx context.Context, sessionKey [sessionpin.SessionKeyLen]byte, role, provider string, expectedStrategy router.Strategy) error {
	q := sqlc.New(r.tx)
	return q.DisableSessionPinProvider(ctx, sqlc.DisableSessionPinProviderParams{
		SessionKey:              sessionKey[:],
		Role:                    role,
		Provider:                provider,
		ExpectedRoutingStrategy: string(expectedStrategy),
	})
}

func (r *SessionPinRepo) SweepExpired(ctx context.Context) error {
	q := sqlc.New(r.tx)
	return q.SweepExpiredSessionPins(ctx)
}

func toSessionPin(row sqlc.RouterSessionPin) sessionpin.Pin {
	pin := sessionpin.Pin{
		Role:                      row.Role,
		InstallationID:            row.InstallationID,
		Provider:                  row.PinnedProvider,
		Model:                     row.PinnedModel,
		Effort:                    row.PinnedEffort,
		PairedProvider:            row.PairedProvider,
		PairedModel:               row.PairedModel,
		Reason:                    row.DecisionReason,
		Strategy:                  router.Strategy(row.RoutingStrategy),
		PolicyGroup:               row.PolicyGroup,
		TurnCount:                 int(row.TurnCount),
		PinnedUntil:               timestampOrZero(row.PinnedUntil),
		FirstPinnedAt:             timestampOrZero(row.FirstPinnedAt),
		LastSeenAt:                timestampOrZero(row.LastSeenAt),
		LastInputTokens:           int(row.LastInputTokens),
		LastCachedReadTokens:      int(row.LastCachedReadTokens),
		LastCachedWriteTokens:     int(row.LastCachedWriteTokens),
		LastOutputTokens:          int(row.LastOutputTokens),
		LastTurnEndedAt:           timestamptzOrZero(row.LastTurnEndedAt),
		LastServedModel:           row.LastServedModel,
		HasEverSwitched:           row.HasEverSwitched,
		ConsecutiveUpstreamErrors: int(row.ConsecutiveUpstreamErrors),
		ConsecutiveOverloadErrors: int(row.ConsecutiveOverloadErrors),
		DisabledProviders:         row.DisabledProviders,
	}
	// Bounded copy guards against a corrupt row panicking the request handler.
	copy(pin.SessionKey[:], row.SessionKey)
	return pin
}

// timestamptzOrZero mirrors timestampOrZero for TIMESTAMPTZ columns:
// NULL becomes the zero value instead of a pointer.
func timestamptzOrZero(t pgtype.Timestamptz) time.Time {
	if !t.Valid {
		return time.Time{}
	}
	return t.Time
}
