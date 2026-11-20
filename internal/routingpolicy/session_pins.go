package routingpolicy

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"
)

type SessionPins struct {
	store    sessionpin.Store
	now      func() time.Time
	strategy router.Strategy
}

func NewSessionPins(store sessionpin.Store, now func() time.Time, strategy router.Strategy) SessionPins {
	return SessionPins{store: store, now: now, strategy: strategy}
}

const PinSessionTTL = time.Hour

var PinNeverExpires = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)

func PinExpiry(reason string, now time.Time) time.Time {
	if strings.HasPrefix(reason, translate.ReasonUserForceModel) {
		return PinNeverExpires
	}
	return now.Add(PinSessionTTL)
}

func PinMatchesEffectiveStrategy(expected router.Strategy, pin sessionpin.Pin) bool {
	if IsUserForcedReason(pin.Reason) {
		return true
	}
	if pin.Strategy == expected {
		return true
	}
	return pin.Strategy == "" && !router.IsBetaStrategy(expected)
}

func DecisionPolicyGroup(dec router.Decision) string {
	if dec.Metadata == nil {
		return ""
	}
	return dec.Metadata.PolicyGroup
}

func HMMHistoryRole(role string) string {
	if role == "" {
		role = sessionpin.DefaultRole
	}
	return role + "_hmm_history"
}

func IsUserForcedReason(reason string) bool {
	return strings.HasPrefix(reason, translate.ReasonUserForceModel)
}

func (sp SessionPins) Load(ctx context.Context, sessionKey [sessionpin.SessionKeyLen]byte, role string) (sessionpin.Pin, bool) {
	pin, active, _ := sp.LoadWithStoreState(ctx, sessionKey, role)
	return pin, active
}

func (sp SessionPins) LoadWithStoreState(ctx context.Context, sessionKey [sessionpin.SessionKeyLen]byte, role string) (_ sessionpin.Pin, active, noStoredState bool) {
	log := observability.FromContext(ctx)
	log.Debug("loadPin called", "role", role, "session_key_hex", fmt.Sprintf("%x", sessionKey))
	pin, found, err := sp.store.Get(ctx, sessionKey, role)
	if err != nil {
		log.Error("session pin store unavailable; falling through to cluster scorer", "err", err)
		return sessionpin.Pin{}, false, false
	}
	if !found {
		return sessionpin.Pin{}, false, true
	}
	if !PinMatchesEffectiveStrategy(sp.strategy, pin) {
		return sessionpin.Pin{}, false, false
	}
	if !pin.PinnedUntil.After(sp.now()) {
		return pin, false, false
	}
	return pin, true, false
}

func (sp SessionPins) LoadHMMHistory(ctx context.Context, sessionKey [sessionpin.SessionKeyLen]byte, role string) sessionpin.Pin {
	log := observability.FromContext(ctx)
	pin, found, err := sp.store.Get(ctx, sessionKey, HMMHistoryRole(role))
	if err != nil {
		log.Error("HMM switch-history store unavailable", "err", err)
		return sessionpin.Pin{}
	}
	if !found {
		return sessionpin.Pin{}
	}
	if !PinMatchesEffectiveStrategy(sp.strategy, pin) {
		return sessionpin.Pin{}
	}
	return pin
}

func (sp SessionPins) Refresh(ctx context.Context, installationID uuid.UUID, sessionKey [sessionpin.SessionKeyLen]byte, existing sessionpin.Pin, role string, chosen router.Decision) {
	sp.RefreshVotes(ctx, installationID, sessionKey, existing, role, chosen, 0, 0)
}

func (sp SessionPins) RefreshDowngradeVotes(ctx context.Context, installationID uuid.UUID, sessionKey [sessionpin.SessionKeyLen]byte, existing sessionpin.Pin, role string, chosen router.Decision, downgradeVotes int) {
	sp.RefreshVotes(ctx, installationID, sessionKey, existing, role, chosen, downgradeVotes, 0)
}

func (sp SessionPins) RefreshVotes(ctx context.Context, installationID uuid.UUID, sessionKey [sessionpin.SessionKeyLen]byte, existing sessionpin.Pin, role string, chosen router.Decision, downgradeVotes, upgradeVotes int) {
	if installationID == uuid.Nil {
		return
	}
	effort := chosen.Effort
	if effort == "" && chosen.Model == existing.Model {
		effort = existing.Effort
	}
	p := sessionpin.Pin{
		SessionKey:            sessionKey,
		Role:                  role,
		InstallationID:        installationID,
		Provider:              chosen.Provider,
		Model:                 chosen.Model,
		Effort:                effort,
		PairedProvider:        existing.PairedProvider,
		PairedModel:           existing.PairedModel,
		Reason:                chosen.Reason,
		Strategy:              sp.strategy,
		PolicyGroup:           existing.PolicyGroup,
		TurnCount:             1,
		PinnedUntil:           PinExpiry(chosen.Reason, sp.now()),
		LastInputTokens:       existing.LastInputTokens,
		LastCachedReadTokens:  existing.LastCachedReadTokens,
		LastCachedWriteTokens: existing.LastCachedWriteTokens,
		LastOutputTokens:      existing.LastOutputTokens,
		LastTurnEndedAt:       existing.LastTurnEndedAt,
		LastOutputLimitAt:     existing.LastOutputLimitAt,
		LastServedModel:       existing.LastServedModel,

		ConsecutiveDowngradeVotes: downgradeVotes,
		ConsecutiveUpgradeVotes:   upgradeVotes,
	}
	sp.Upsert(ctx, p)
}

func (sp SessionPins) WriteNew(ctx context.Context, installationID uuid.UUID, sessionKey [sessionpin.SessionKeyLen]byte, role string, chosen router.Decision) {
	log := observability.FromContext(ctx)
	var pairedProvider, pairedModel string
	if chosen.Metadata != nil {
		pairedProvider = chosen.Metadata.PairedProvider
		pairedModel = chosen.Metadata.PairedModel
	}
	log.Info("writeNewPin called", "installation_id", installationID.String(), "role", role, "model", chosen.Model, "paired_model", pairedModel, "paired_provider", pairedProvider, "session_key_hex", fmt.Sprintf("%x", sessionKey))
	if installationID == uuid.Nil {
		log.Info("writeNewPin: skipping because installationID is uuid.Nil")
		return
	}
	p := sessionpin.Pin{
		SessionKey:     sessionKey,
		Role:           role,
		InstallationID: installationID,
		Provider:       chosen.Provider,
		Model:          chosen.Model,
		Effort:         chosen.Effort,
		PairedProvider: pairedProvider,
		PairedModel:    pairedModel,
		Reason:         chosen.Reason,
		Strategy:       sp.strategy,
		PolicyGroup:    DecisionPolicyGroup(chosen),
		TurnCount:      1,
		PinnedUntil:    PinExpiry(chosen.Reason, sp.now()),
	}
	sp.Upsert(ctx, p)
}

func (sp SessionPins) Upsert(ctx context.Context, p sessionpin.Pin) {
	log := observability.FromContext(ctx)
	if p.Strategy == "" {
		p.Strategy = sp.strategy
	}
	if err := sp.store.Upsert(context.WithoutCancel(ctx), p); err != nil {
		log.Error("session pin upsert failed", "err", err)
		return
	}
	log.Debug("session pin upsert ok", "installation_id", p.InstallationID.String(), "role", p.Role, "model", p.Model)
}

const (
	forceModelHistoryRoleSuffix = "_force_hist"
	forceModelHistoryReason     = "force_model_history"
)

func ForceModelHistoryRole(role string) string {
	if role == "" {
		role = sessionpin.DefaultRole
	}
	return role + forceModelHistoryRoleSuffix
}

func (sp SessionPins) AnchorForceModelHistory(
	ctx context.Context,
	installationID uuid.UUID,
	sessionKey [sessionpin.SessionKeyLen]byte,
	role string,
	forcedPin sessionpin.Pin,
) {
	if sp.store == nil || installationID == uuid.Nil {
		return
	}
	sp.Upsert(ctx, sessionpin.Pin{
		SessionKey:     sessionKey,
		Role:           ForceModelHistoryRole(role),
		InstallationID: installationID,
		Provider:       forcedPin.Provider,
		Model:          forcedPin.Model,
		Reason:         forceModelHistoryReason,
		Strategy:       sp.strategy,
		TurnCount:      1,
		PinnedUntil:    PinNeverExpires,
	})
}
