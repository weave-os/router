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

const pinEvictionStrikeThreshold = 2

const (
	pinEvictionReasonSubscriptionPool  = "subscription_pool_exhausted"
	pinEvictionReasonSubscriptionModel = "subscription_model_unavailable"
)

func (s *Service) expireSessionPin(
	ctx context.Context, strategy router.Strategy,
	installationID uuid.UUID,
	sessionKey [sessionpin.SessionKeyLen]byte,
	role string,
	reason string,
) error {
	return s.expireSessionPinRow(ctx, strategy, installationID, sessionKey, role, reason, true)
}

func (s *Service) expireSessionPinRow(
	ctx context.Context, strategy router.Strategy,
	installationID uuid.UUID,
	sessionKey [sessionpin.SessionKeyLen]byte,
	role string,
	reason string,
	invalidateContinuation bool,
) error {
	expired := expiredSessionPin(installationID, sessionKey, role, reason, strategy, s.clock.Now())
	if err := s.pinStore.Upsert(context.WithoutCancel(ctx), expired); err != nil {
		return err
	}
	if !invalidateContinuation {
		return nil
	}
	return postCommandContinuation(s, strategy).Invalidate(ctx, sessionKey, role)
}

func expiredSessionPin(
	installationID uuid.UUID,
	sessionKey [sessionpin.SessionKeyLen]byte,
	role string,
	reason string,
	strategy router.Strategy,
	now time.Time,
) sessionpin.Pin {
	return sessionpin.Pin{
		SessionKey:     sessionKey,
		Role:           role,
		InstallationID: installationID,
		Provider:       "",
		Model:          "",
		Reason:         reason,
		Strategy:       strategy,
		TurnCount:      1,
		PinnedUntil:    now.Add(-time.Second),
	}
}

func (s *Service) expireSessionPinAndHMMHistory(
	ctx context.Context, strategy router.Strategy,
	installationID uuid.UUID,
	sessionKey [sessionpin.SessionKeyLen]byte,
	role string,
	reason string,
) error {
	if err := s.expireSessionPin(ctx, strategy, installationID, sessionKey, role, reason); err != nil {
		return err
	}
	historyRole := hmmHistoryRole(role)
	if historyRole == role {
		return nil
	}
	return s.expireSessionPinRow(ctx, strategy, installationID, sessionKey, historyRole, reason, false)
}

func (s *Service) evictPinAfterDegenerateResponse(
	ctx context.Context, strategy router.Strategy,
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

	if err := s.expireSessionPin(ctx, strategy, installationID, sessionKey, role, "degenerate_response"); err != nil {
		log.Error("pin eviction after degenerate response failed", "err", err, "role", role)
		return
	}
	log.Info("session pin evicted after degenerate response",
		"role", role,
	)
}

func (s *Service) maybeExpireDeadArmPin(
	ctx context.Context, strategy router.Strategy,
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
	if err := s.expireSessionPin(ctx, strategy, installationID, sessionKey, role, "dead_arm_rejected"); err != nil {
		log.Error("pin eviction after dead-arm rejection failed", "err", err, "role", role)
	}
}

func (s *Service) maybeExpireSubscriptionArmPin(
	ctx context.Context, strategy router.Strategy,
	failure error,
	decisionReason string,
	installationID uuid.UUID,
	sessionKey [sessionpin.SessionKeyLen]byte,
	role string,
) {
	reason := subscriptionArmPinEvictionReason(failure)
	if reason == "" || s.pinStore == nil || installationID == uuid.Nil || sessionKey == ([sessionpin.SessionKeyLen]byte{}) || strings.HasPrefix(decisionReason, translate.ReasonUserForceModel) {
		return
	}
	log := observability.FromContext(ctx)
	if err := s.expireSessionPin(ctx, strategy, installationID, sessionKey, role, reason); err != nil {
		log.Error("pin eviction after subscription arm became unavailable failed", "err", err, "role", role, "reason", reason)
		return
	}
	log.Info("session pin evicted after subscription arm became unavailable",
		"role", role,
		"reason", reason,
	)
}

func subscriptionArmPinEvictionReason(err error) string {
	switch {
	case isSubscriptionPoolError(err):
		return pinEvictionReasonSubscriptionPool
	case anthropicSubscriptionModelRejected(err):
		return pinEvictionReasonSubscriptionModel
	default:
		return ""
	}
}

func (s *Service) maybeEvictPinAfterUpstreamErr(
	ctx context.Context,
	stickyHit bool,
	proxyErr error,
	decisionReason string,
	target sessionTarget,
	role string,
) {
	if !stickyHit || s.pinStore == nil || target.installationID == uuid.Nil {
		return
	}
	if target.sessionKey == ([sessionpin.SessionKeyLen]byte{}) {
		return
	}
	if strings.HasPrefix(decisionReason, translate.ReasonUserForceModel) {
		return
	}

	log := observability.FromContext(ctx)

	if proxyErr == nil {
		if err := s.pinStore.ResetUpstreamErrors(context.WithoutCancel(ctx), target.sessionKey, role, target.strategy); err != nil {
			log.Error("pin error-counter reset failed", "err", err, "role", role)
		}
		return
	}

	status := upstreamStatus(proxyErr)
	if status == 0 {
		return
	}
	if providers.IsRetryableStatus(status) {
		return
	}

	count, err := s.pinStore.IncrementUpstreamErrors(context.WithoutCancel(ctx), target.sessionKey, role, target.strategy)
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

	if err := s.expireSessionPin(ctx, target.strategy, target.installationID, target.sessionKey, role, "upstream_error_strike_threshold"); err != nil {
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
