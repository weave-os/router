package hmm

import (
	"context"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/continuity"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/routingpolicy"
)

func HistoryRole(role string) string {
	if role == "" {
		role = sessionpin.DefaultRole
	}
	return role + "_hmm_history"
}

func LoadHistory(ctx context.Context, rows continuity.HistoryRows, sessionKey [sessionpin.SessionKeyLen]byte, role string) sessionpin.Pin {
	log := observability.FromContext(ctx)
	pin, found, err := rows.Store.Get(ctx, sessionKey, HistoryRole(role))
	if err != nil {
		log.Error("HMM switch-history store unavailable", "err", err)
		return sessionpin.Pin{}
	}
	if !found {
		return sessionpin.Pin{}
	}
	if !routingpolicy.PinMatchesEffectiveStrategy(rows.Strategy, pin) {
		return sessionpin.Pin{}
	}
	return pin
}

func RecordTurnHistory(ctx context.Context, rows continuity.HistoryRows, rec continuity.TurnHistoryRecord) (role string, usageRecorded bool) {
	usage := rec.Usage
	hasUsage := usage.InputTokens != 0 || usage.OutputTokens != 0 || usage.CachedWriteTokens != 0 || usage.CachedReadTokens != 0
	historyProvider := usage.ServedProvider
	if !hasUsage {
		if prior := LoadHistory(ctx, rows, rec.SessionKey, rec.PinRole); prior.Provider != "" {
			historyProvider = prior.Provider
		}
	}
	role = HistoryRole(rec.PinRole)
	routingpolicy.NewSessionPins(rows.Store, rows.Now, rows.Strategy).Upsert(ctx, sessionpin.Pin{
		SessionKey:     rec.SessionKey,
		Role:           role,
		InstallationID: rec.InstallationID,
		Provider:       historyProvider,
		Reason:         historyStoredReason(rec.Fresh, rec.Decision),
		Strategy:       rows.Strategy,
		TurnCount:      1,
		PinnedUntil:    routingpolicy.PinExpiry(HistoryReason, rows.Now()),
	})
	if !hasUsage {
		return role, false
	}
	usage.Strategy = rows.Strategy
	usage.EndedAt = rows.Now()
	if err := rows.Store.UpdateUsage(ctx, rec.SessionKey, role, usage); err != nil {
		observability.FromContext(ctx).Error("HMM switch-history writeback failed", "err", err)
	}
	return role, true
}

func historyStoredReason(fresh, decision router.Decision) string {
	if IsDecision(fresh) {
		return fresh.Reason
	}
	if IsDecision(decision) {
		return decision.Reason
	}
	return HistoryReason
}
