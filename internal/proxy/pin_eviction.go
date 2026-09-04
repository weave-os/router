package proxy

import (
	"context"
	"strings"
	"time"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
)

// pinEvictionStrikeThreshold is the consecutive-non-retryable-4xx count that
// expires a sticky pin. One strike: a non-retryable 400 (provider grammar/
// schema rejection, capability rejection) is deterministic per-arm, so a
// second attempt on the same pinned model 400s identically — waiting for two
// locks the session onto a dead arm for its lifetime (the 2026-08-21 Fireworks
// "Conflict in schema definitions" lockout). The prompt-cache cost of a single
// spurious eviction is far cheaper than a dead session.
// Generic counter: a single request-specific 400 (validation, malformed body)
// must not throw away a working pin's prompt cache — the deterministic
// dead-arm classes (schema/capability/intrinsically-incompatible) evict
// immediately via maybeExpireDeadArmPin, so this threshold only guards the
// residual non-deterministic 4xx noise.
const pinEvictionStrikeThreshold = 2

// expireSessionPin writes an already-expired sessionpin.Pin so the next
// turn's loadPin discards it and the session re-routes via the cluster
// scorer. Shared by force-model clear, loop-break/no-progress/
// degenerate-response eviction, and the upstream-error strike threshold —
// call sites differ only in the Reason string recorded for observability.
//
// context.Background(): callers invoke this once the response has already
// streamed or is about to be written, so the request ctx may already be
// canceled; the eviction write must still land or the next turn inherits
// the stale pin.
func (s *Service) expireSessionPin(
	ctx context.Context,
	installationID uuid.UUID,
	sessionKey [sessionpin.SessionKeyLen]byte,
	role string,
	reason string,
) error {
	return s.expireSessionPinRow(ctx, installationID, sessionKey, role, reason, true)
}

// expireSessionPinRow writes the expired marker. Only a primary routing pin
// can own a post-command continuation; HMM history rows must not derive a
// second role suffix because router.session_pins.role is bounded.
func (s *Service) expireSessionPinRow(
	ctx context.Context,
	installationID uuid.UUID,
	sessionKey [sessionpin.SessionKeyLen]byte,
	role string,
	reason string,
	invalidateContinuation bool,
) error {
	expired := sessionpin.Pin{
		SessionKey:     sessionKey,
		Role:           role,
		InstallationID: installationID,
		Provider:       "",
		Model:          "",
		Reason:         reason,
		Strategy:       router.StrategyFromContext(ctx),
		TurnCount:      1,
		PinnedUntil:    time.Now().Add(-time.Second),
	}
	if err := s.pinStore.Upsert(context.Background(), expired); err != nil {
		return err
	}
	if !invalidateContinuation {
		return nil
	}
	return s.invalidatePostCommandContinuation(ctx, sessionKey, role)
}

func (s *Service) expireSessionPinAndHMMHistory(
	ctx context.Context,
	installationID uuid.UUID,
	sessionKey [sessionpin.SessionKeyLen]byte,
	role string,
	reason string,
) error {
	if err := s.expireSessionPin(ctx, installationID, sessionKey, role, reason); err != nil {
		return err
	}
	historyRole := hmmHistoryRole(role)
	if historyRole == role {
		return nil
	}
	return s.expireSessionPinRow(ctx, installationID, sessionKey, historyRole, reason, false)
}

// evictPinAfterDegenerateResponse expires the session pin after a degenerate
// response (end_turn, no tool calls, too few output tokens). The current
// turn already streamed and can't be retried, but evicting ensures the next
// turn re-scores instead of repeating the same misbehaving model.
//
// No-ops when there's no decision history yet (!stickyHit), no addressable
// pin row (zero session_key/installation_id), or the pin was user-forced
// (auto-eviction shouldn't override an explicit /force-model). Upsert errors
// are logged and swallowed since eviction is best-effort.
func (s *Service) evictPinAfterDegenerateResponse(
	ctx context.Context,
	stickyHit bool,
	decisionReason string,
	installationID uuid.UUID,
	sessionKey [sessionpin.SessionKeyLen]byte,
	role string,
) {
	if !stickyHit || s.pinStore == nil || installationID == uuid.Nil {
		return
	}
	if sessionKey == ([sessionpin.SessionKeyLen]byte{}) {
		return
	}
	if strings.HasPrefix(decisionReason, translate.ReasonUserForceModel) {
		return
	}

	log := observability.FromContext(ctx)

	if err := s.expireSessionPin(ctx, installationID, sessionKey, role, "degenerate_response"); err != nil {
		log.Error("pin eviction after degenerate response failed", "err", err, "role", role)
		return
	}
	log.Info("session pin evicted after degenerate response",
		"role", role,
	)
}

// maybeExpireDeadArmPin expires the sticky pin when the pinned arm provably
// cannot serve the request shape (schema/capability rejection) even after a
// sibling/baseline rescue. A successful rescue nils proxyErr, so
// maybeEvictPinAfterUpstreamErr resets the strike counter and the dead arm
// stays pinned — every subsequent turn burns another deterministic 400. Never
// expires a user force-model pin (HasPrefix covers the +tier_clamp suffix).
func (s *Service) maybeExpireDeadArmPin(
	ctx context.Context,
	deadArmRejected bool,
	decisionReason string,
	installationID uuid.UUID,
	sessionKey [sessionpin.SessionKeyLen]byte,
	role string,
) {
	if !deadArmRejected || s.pinStore == nil || installationID == uuid.Nil || sessionKey == ([sessionpin.SessionKeyLen]byte{}) || strings.HasPrefix(decisionReason, translate.ReasonUserForceModel) {
		return
	}
	log := observability.FromContext(ctx)
	if err := s.expireSessionPin(ctx, installationID, sessionKey, role, "dead_arm_rejected"); err != nil {
		log.Error("pin eviction after dead-arm rejection failed", "err", err, "role", role)
	}
}

// maybeEvictPinAfterUpstreamErr applies the two-strike eviction policy for a
// turn run against a sticky pin: a successful turn resets the strike counter,
// a non-retryable upstream 4xx increments it, and hitting
// pinEvictionStrikeThreshold expires the pin so the next turn re-routes via
// the cluster scorer.
//
// No-ops when there's no decision history yet (!stickyHit), no addressable
// pin row (zero session_key/installation_id), the pin was user-forced (user
// keeps /unforce-model as the escape hatch), or the status is retryable
// (408/429/5xx — handled by dispatchWithFallback's retry loop). Errors from
// the increment/reset/upsert are logged and swallowed since eviction is
// best-effort and must not change the client-visible outcome.
func (s *Service) maybeEvictPinAfterUpstreamErr(
	ctx context.Context,
	stickyHit bool,
	proxyErr error,
	decisionReason string,
	installationID uuid.UUID,
	sessionKey [sessionpin.SessionKeyLen]byte,
	role string,
) {
	if !stickyHit || s.pinStore == nil || installationID == uuid.Nil {
		return
	}
	if sessionKey == ([sessionpin.SessionKeyLen]byte{}) {
		return
	}
	// Prefix check covers both ReasonUserForceModel and its tier_clamp suffix.
	if strings.HasPrefix(decisionReason, translate.ReasonUserForceModel) {
		return
	}

	log := observability.FromContext(ctx)

	if proxyErr == nil {
		// context.Background(): the request ctx is already canceled by the
		// time streaming finishes, but this reset must still go through.
		if err := s.pinStore.ResetUpstreamErrors(context.Background(), sessionKey, role, router.StrategyFromContext(ctx)); err != nil {
			log.Error("pin error-counter reset failed", "err", err, "role", role)
		}
		return
	}

	status := upstreamStatus(proxyErr)
	if status == 0 {
		// Non-upstream error (transport blowup, deadline, etc.) — not a
		// model-quality signal; leave the counter alone.
		return
	}
	if providers.IsRetryableStatus(status) {
		return
	}

	count, err := s.pinStore.IncrementUpstreamErrors(context.Background(), sessionKey, role, router.StrategyFromContext(ctx))
	if err != nil {
		log.Error("pin error-counter increment failed", "err", err, "role", role, "upstream_status", status)
		return
	}
	if count < pinEvictionStrikeThreshold {
		log.Debug("pin error-counter incremented",
			"role", role,
			"upstream_status", status,
			"consecutive_errors", count,
			"strike_threshold", pinEvictionStrikeThreshold,
		)
		return
	}

	// Expire via a PinnedUntil in the past, same pattern as loop-break /
	// no-progress / force-model, so loadPin discards it next turn.
	if err := s.expireSessionPin(ctx, installationID, sessionKey, role, "upstream_error_strike_threshold"); err != nil {
		log.Error("pin eviction upsert failed", "err", err, "role", role)
		return
	}
	log.Info("session pin evicted after consecutive upstream errors",
		"role", role,
		"upstream_status", status,
		"consecutive_errors", count,
		"strike_threshold", pinEvictionStrikeThreshold,
	)
}
