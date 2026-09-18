package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
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
var _ sessionpin.CooldownStore = (*SessionPinRepo)(nil)

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
		SessionKey:                p.SessionKey[:],
		Role:                      p.Role,
		InstallationID:            p.InstallationID,
		PinnedProvider:            p.Provider,
		PinnedModel:               p.Model,
		PinnedEffort:              p.Effort,
		PairedProvider:            p.PairedProvider,
		PairedModel:               p.PairedModel,
		DecisionReason:            p.Reason,
		RoutingStrategy:           string(p.Strategy),
		PolicyGroup:               p.PolicyGroup,
		TurnCount:                 int32(p.TurnCount),
		PinnedUntil:               pgtype.Timestamp{Time: p.PinnedUntil.UTC(), Valid: true},
		ConsecutiveDowngradeVotes: int32(p.ConsecutiveDowngradeVotes),
		ConsecutiveUpgradeVotes:   int32(p.ConsecutiveUpgradeVotes),
	})
}

// UpdateUsage records the previous turn's usage on the pin row. A missing
// pin (evicted/swept/never created) is a no-op, not an error. A zero
// EndedAt is stamped with time.Now when the caller omits it; the output-limit
// marker is written from that same instant in the same statement.
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
		LastTurnEndedAt:         pgtype.Timestamptz{Time: endedAt.UTC(), Valid: !endedAt.IsZero()},
		OutputLimitReached:      usage.OutputLimitReached,
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

// ExpireAndDemoteModel seeds or expires the (session_key, role) row and
// appends model to demoted_models in one statement. The ON CONFLICT update is
// guarded by expired.Strategy, so a row another strategy owns is not touched.
func (r *SessionPinRepo) ExpireAndDemoteModel(ctx context.Context, expired sessionpin.Pin, model string, _ sessionpin.DemotionReason) error {
	q := sqlc.New(r.tx)
	return q.ExpireAndDemoteSessionPinModel(ctx, sqlc.ExpireAndDemoteSessionPinModelParams{
		SessionKey:              expired.SessionKey[:],
		Role:                    expired.Role,
		InstallationID:          expired.InstallationID,
		DecisionReason:          expired.Reason,
		ExpectedRoutingStrategy: string(expired.Strategy),
		PinnedUntil:             pgtype.Timestamp{Time: expired.PinnedUntil.UTC(), Valid: true},
		Model:                   model,
	})
}

func (r *SessionPinRepo) ExpireAndCoolDownModel(ctx context.Context, expired sessionpin.Pin, model string, until time.Time, _ sessionpin.DemotionReason) error {
	q := sqlc.New(r.tx)
	return q.ExpireAndCoolDownSessionPinModel(ctx, sqlc.ExpireAndCoolDownSessionPinModelParams{
		SessionKey:              expired.SessionKey[:],
		Role:                    expired.Role,
		InstallationID:          expired.InstallationID,
		DecisionReason:          expired.Reason,
		ExpectedRoutingStrategy: string(expired.Strategy),
		PinnedUntil:             pgtype.Timestamp{Time: expired.PinnedUntil.UTC(), Valid: true},
		Model:                   model,
		CooldownUntil:           pgtype.Timestamptz{Time: until.UTC(), Valid: true},
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
		LastOutputLimitAt:         timestamptzOrZero(row.LastOutputLimitAt),
		LastServedModel:           row.LastServedModel,
		HasEverSwitched:           row.HasEverSwitched,
		ConsecutiveUpstreamErrors: int(row.ConsecutiveUpstreamErrors),
		ConsecutiveOverloadErrors: int(row.ConsecutiveOverloadErrors),
		ConsecutiveDowngradeVotes: int(row.ConsecutiveDowngradeVotes),
		ConsecutiveUpgradeVotes:   int(row.ConsecutiveUpgradeVotes),
		DisabledProviders:         row.DisabledProviders,
		DemotedModels:             row.DemotedModels,
		DemotionCooldowns:         demotionCooldowns(row.DemotionCooldowns),
	}
	// Bounded copy guards against a corrupt row panicking the request handler.
	copy(pin.SessionKey[:], row.SessionKey)
	return pin
}

// demotionCooldowns decodes the {model: RFC 3339 instant} JSONB column. An
// unreadable value yields no cooldowns rather than failing the pin read: the
// worst outcome is one turn that does not skip a throttled arm.
func demotionCooldowns(raw []byte) map[string]time.Time {
	if len(raw) == 0 {
		return nil
	}
	var cooldowns map[string]time.Time
	if err := json.Unmarshal(raw, &cooldowns); err != nil || len(cooldowns) == 0 {
		return nil
	}
	return cooldowns
}

// timestamptzOrZero mirrors timestampOrZero for TIMESTAMPTZ columns:
// NULL becomes the zero value instead of a pointer.
func timestamptzOrZero(t pgtype.Timestamptz) time.Time {
	if !t.Valid {
		return time.Time{}
	}
	return t.Time
}
