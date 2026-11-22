package routingpolicy

import (
	"context"

	"github.com/google/uuid"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router/sessionpin"
)

type HMMTurnRecord struct {
	SessionKey     [sessionpin.SessionKeyLen]byte
	InstallationID uuid.UUID
	PinRole        string
	Reason         string
	HistoryReason  string
	Usage          sessionpin.Usage
}

func (sp SessionPins) RecordHMMTurn(ctx context.Context, rec HMMTurnRecord) (role string, usageRecorded bool) {
	usage := rec.Usage
	hasUsage := usage.InputTokens != 0 || usage.OutputTokens != 0 || usage.CachedWriteTokens != 0 || usage.CachedReadTokens != 0
	historyProvider := usage.ServedProvider
	if !hasUsage {
		if prior := sp.LoadHMMHistory(ctx, rec.SessionKey, rec.PinRole); prior.Provider != "" {
			historyProvider = prior.Provider
		}
	}
	role = HMMHistoryRole(rec.PinRole)
	sp.Upsert(ctx, sessionpin.Pin{
		SessionKey:     rec.SessionKey,
		Role:           role,
		InstallationID: rec.InstallationID,
		Provider:       historyProvider,
		Reason:         rec.Reason,
		Strategy:       sp.strategy,
		TurnCount:      1,
		PinnedUntil:    PinExpiry(rec.HistoryReason, sp.now()),
	})
	if !hasUsage {
		return role, false
	}
	usage.Strategy = sp.strategy
	usage.EndedAt = sp.now()
	if err := sp.store.UpdateUsage(ctx, rec.SessionKey, role, usage); err != nil {
		observability.FromContext(ctx).Error("HMM switch-history writeback failed", "err", err)
	}
	return role, true
}
