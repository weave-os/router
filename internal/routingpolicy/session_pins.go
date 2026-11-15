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

// SessionPins reads and writes the session-sticky routing pins.
type SessionPins struct{ store sessionpin.Store }

// NewSessionPins wraps store.
func NewSessionPins(store sessionpin.Store) SessionPins { return SessionPins{store: store} }

// PinSessionTTL mirrors Anthropic's prompt-cache TTL on Sonnet/Haiku/Opus 4.5+
// so the pin lifecycle tracks the cache it's keeping warm.
const PinSessionTTL = time.Hour

// PinNeverExpires is the sentinel PinnedUntil for user-forced pins: a
// /force-model must survive arbitrarily long idle gaps and only clear on
// /unforce-model, never lapse on the session TTL. Far enough out to read as
// live indefinitely everywhere PinnedUntil is checked, but still within
// Postgres's timestamp range. /unforce-model rewrites it to a past time.
var PinNeverExpires = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)

// PinExpiry returns the PinnedUntil to record for a pin with the given decision
// reason. User-forced pins get the never-expires sentinel; every other pin keeps
// the sliding one-hour session TTL.
func PinExpiry(reason string) time.Time {
	if strings.HasPrefix(reason, translate.ReasonUserForceModel) {
		return PinNeverExpires
	}
	return time.Now().Add(PinSessionTTL)
}

// PinMatchesEffectiveStrategy reports whether a stored pin belongs to the
// strategy serving this request. An explicit user force is policy-independent;
// legacy empty-strategy rows remain eligible outside beta during rollout.
func PinMatchesEffectiveStrategy(ctx context.Context, pin sessionpin.Pin) bool {
	if IsUserForcedReason(pin.Reason) {
		return true
	}
	expected := router.StrategyFromContext(ctx)
	if pin.Strategy == expected {
		return true
	}
	return pin.Strategy == "" && !router.IsBetaStrategy(expected)
}

// DecisionPolicyGroup returns the policy cluster/group a decision was drawn
// from, or "" for reconstructed pins and routers that report no group.
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

// loadPin returns the stored pin and whether it may actively serve this turn.
// Expired rows are misses for routing, but their history fields still protect
// Anthropic emit from stale thinking-block signatures in the client transcript.
func (sp SessionPins) Load(ctx context.Context, sessionKey [sessionpin.SessionKeyLen]byte, role string) (sessionpin.Pin, bool) {
	pin, active, _ := sp.LoadWithStoreState(ctx, sessionKey, role)
	return pin, active
}

// loadPinWithStoreState is loadPin plus noStoredState: the store answered and
// held nothing for this key. A store error routes exactly like a miss (fall
// through to the scorer), but a caller that reads absence as session state —
// first-turn telemetry capture — must not read an outage as a fresh session.
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
	if !PinMatchesEffectiveStrategy(ctx, pin) {
		return sessionpin.Pin{}, false, false
	}
	if !pin.PinnedUntil.After(time.Now()) {
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
	if !PinMatchesEffectiveStrategy(ctx, pin) {
		return sessionpin.Pin{}
	}
	return pin
}

// refreshPin extends the TTL on an existing pin. Carries the existing pin's
// usage forward so the planner has evidence before the next UpdateUsage
// writeback lands.
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
		SessionKey:     sessionKey,
		Role:           role,
		InstallationID: installationID,
		Provider:       chosen.Provider,
		Model:          chosen.Model,
		Effort:         effort,
		// No scorer runs on a plain refresh, so carry the existing pair
		// forward unchanged (ON CONFLICT preserves an empty one).
		PairedProvider: existing.PairedProvider,
		PairedModel:    existing.PairedModel,
		Reason:         chosen.Reason,
		Strategy:       router.StrategyFromContext(ctx),
		// Same rationale as the pair above: a refresh runs no policy, so the
		// reconstructed decision carries no group. Carry the stored one forward.
		PolicyGroup:           existing.PolicyGroup,
		TurnCount:             1,
		PinnedUntil:           PinExpiry(chosen.Reason),
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

// writeNewPin records a freshly-routed decision as the active pin. Used on
// first-turn routing and switch turns. UpdateUsage fills in usage stats later.
func (sp SessionPins) WriteNew(ctx context.Context, installationID uuid.UUID, sessionKey [sessionpin.SessionKeyLen]byte, role string, chosen router.Decision) {
	log := observability.FromContext(ctx)
	// pinDecision(pin) reconstructions carry no Metadata, so the nil guard
	// leaves the pair empty; ON CONFLICT then preserves the stored pair
	// instead of wiping it.
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
		Strategy:       router.StrategyFromContext(ctx),
		PolicyGroup:    DecisionPolicyGroup(chosen),
		TurnCount:      1,
		PinnedUntil:    PinExpiry(chosen.Reason),
	}
	sp.Upsert(ctx, p)
}

// upsertPin synchronously persists a pin write. context.Background() is used
// so the DB write survives request-ctx cancellation after the response has
// finished streaming.
func (sp SessionPins) Upsert(ctx context.Context, p sessionpin.Pin) {
	log := observability.FromContext(ctx)
	if p.Strategy == "" {
		p.Strategy = router.StrategyFromContext(ctx)
	}
	if err := sp.store.Upsert(context.Background(), p); err != nil {
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
		Strategy:       router.StrategyFromContext(ctx),
		TurnCount:      1,
		PinnedUntil:    PinNeverExpires,
	})
}
