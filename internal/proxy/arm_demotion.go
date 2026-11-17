package proxy

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
)

func (s *Service) maybeDemoteArmAfterCommittedStreamFailure(
	ctx context.Context,
	committedStream bool,
	hardPinned bool,
	proxyErr error,
	model string,
	decisionReason string,
	installationID uuid.UUID,
	sessionKey [sessionpin.SessionKeyLen]byte,
	role string,
	pinRole string,
) string {
	if !s.ResolveCommittedStreamArmDemotion(ctx) || s.pinStore == nil || installationID == uuid.Nil {
		return ""
	}
	if sessionKey == ([sessionpin.SessionKeyLen]byte{}) || model == "" || hardPinned {
		return ""
	}
	if strings.HasPrefix(decisionReason, translate.ReasonUserForceModel) {
		return ""
	}
	if !committedStream || !isCommittedStreamFailure(ctx, proxyErr) {
		return ""
	}
	if !s.demoteArmForSession(ctx, model, sessionpin.DemotionReasonCommittedStreamFailure, time.Time{}, upstreamStatus(proxyErr), installationID, sessionKey, role, pinRole) {
		return ""
	}
	return model
}

func (s *Service) maybeDemoteArmAfterRescuedFailure(
	ctx context.Context,
	rescueRan bool,
	hardPinned bool,
	primaryErr error,
	primary router.Decision,
	installationID uuid.UUID,
	sessionKey [sessionpin.SessionKeyLen]byte,
) string {
	model, _ := s.maybeStrikeArmAfterRescuedFailure(ctx, rescueRan, hardPinned, primaryErr, primary, installationID, sessionKey, sessionpin.DefaultRole, sessionpin.DefaultRole)
	return model
}

func (s *Service) maybeStrikeArmAfterRescuedFailure(
	ctx context.Context,
	rescueRan bool,
	hardPinned bool,
	primaryErr error,
	primary router.Decision,
	installationID uuid.UUID,
	sessionKey [sessionpin.SessionKeyLen]byte,
	role string,
	pinRole string,
) (string, sessionpin.DemotionReason) {
	if s.pinStore == nil || installationID == uuid.Nil {
		return "", ""
	}
	if sessionKey == ([sessionpin.SessionKeyLen]byte{}) || primary.Model == "" || hardPinned {
		return "", ""
	}
	if strings.HasPrefix(primary.Reason, translate.ReasonUserForceModel) {
		return "", ""
	}
	headerTimeout := providers.IsResponseHeaderTimeout(primaryErr)
	if (!rescueRan && !headerTimeout) || (rescueRan && !isRescuedPrimaryFailure(primaryErr)) {
		return "", ""
	}
	if !s.ResolveRescuedFailureArmDemotion(ctx) {
		if rescueRan {
			recordTurnFailure(ctx, primary.Model)
		}
		return "", ""
	}
	reason, cooldownUntil := sessionpin.DemotionReasonRescuedFailure, time.Time{}
	if headerTimeout {
		reason = sessionpin.DemotionReasonResponseHeaderTimeout
	} else if s.ResolveTransientRateLimit(ctx) && isTransientGatewayFailure(primaryErr) {
		cooldown := s.ResolveRateLimitCooldown(ctx)
		reason, cooldownUntil = sessionpin.DemotionReasonTransientFailure, s.now.Now().Add(cooldown)
		dispatch.ThrottleLedgerFromContext(ctx).RecordCooldown(cooldownUntil, cooldown)
	} else if s.ResolveTransientRateLimit(ctx) && isRateLimitedPrimaryFailure(primaryErr) {
		cooldown := s.ResolveRateLimitCooldown(ctx)
		reason, cooldownUntil = sessionpin.DemotionReasonRateLimited, s.now.Now().Add(cooldown)
		dispatch.ThrottleLedgerFromContext(ctx).RecordCooldown(cooldownUntil, cooldown)
	}
	if !s.demoteArmForSession(ctx, primary.Model, reason, cooldownUntil, upstreamStatus(primaryErr), installationID, sessionKey, role, pinRole) {
		return "", ""
	}
	return primary.Model, reason
}

func isTransientGatewayFailure(err error) bool {
	switch upstreamStatus(err) {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func (s *Service) maybeDemoteArmAfterUnrescuedStall(
	ctx context.Context,
	rescueRan, committed, hardPinned bool,
	stallErr error,
	failed router.Decision,
	installationID uuid.UUID,
	sessionKey [sessionpin.SessionKeyLen]byte,
	role, pinRole string,
) string {
	if !s.ResolveRescuedFailureArmDemotion(ctx) || s.pinStore == nil || installationID == uuid.Nil ||
		sessionKey == ([sessionpin.SessionKeyLen]byte{}) || failed.Model == "" || hardPinned || rescueRan || committed || ctx.Err() != nil ||
		strings.HasPrefix(failed.Reason, translate.ReasonUserForceModel) || !isUpstreamWatchdogError(stallErr) {
		return ""
	}
	if !s.demoteArmForSession(ctx, failed.Model, sessionpin.DemotionReasonUnrescuedStall, time.Time{}, upstreamStatus(stallErr), installationID, sessionKey, role, pinRole) {
		return ""
	}
	return failed.Model
}

func (s *Service) demoteArmForSession(
	ctx context.Context,
	model string,
	reason sessionpin.DemotionReason,
	cooldownUntil time.Time,
	primaryStatus int,
	installationID uuid.UUID,
	sessionKey [sessionpin.SessionKeyLen]byte,
	role string,
	pinRole string,
) bool {
	log := observability.FromContext(ctx)
	if clusterAllowlistsPinModel(clusterArmOverridesForRequest(ctx), model) {
		log.Info("model kept for session despite upstream failure: cluster allowlists name no other model",
			"role", role,
			"model", model,
			"reason", string(reason),
			"upstream_status", primaryStatus,
		)
		return false
	}

	if pinRole == "" {
		pinRole = sessionpin.DefaultRole
	}
	if role == "" {
		role = sessionpin.DefaultRole
	}
	strategy := router.StrategyFromContext(ctx)
	cooldownStore, hasCooldownStore := s.pinStore.(sessionpin.CooldownStore)
	if !cooldownUntil.IsZero() && !hasCooldownStore {
		log.Warn("session pin store cannot persist a cooldown, demoting for the session", "model", model, "reason", string(reason))
		cooldownUntil = time.Time{}
	}
	for _, strikeRole := range demotionRoles(role, pinRole) {
		expired := expiredSessionPin(installationID, sessionKey, strikeRole, string(reason), strategy, s.now.Now())
		var err error
		if cooldownUntil.IsZero() {
			err = s.pinStore.ExpireAndDemoteModel(context.WithoutCancel(ctx), expired, model, reason)
		} else {
			err = cooldownStore.ExpireAndCoolDownModel(context.WithoutCancel(ctx), expired, model, cooldownUntil, reason)
		}
		if err != nil {
			log.Error("session model demotion failed", "err", err, "role", strikeRole, "model", model, "reason", string(reason))
			return false
		}
	}
	if err := s.invalidatePostCommandContinuation(ctx, sessionKey, pinRole); err != nil {
		log.Error("continuation invalidation after session model demotion failed", "err", err, "role", role, "pin_role", pinRole, "model", model, "reason", string(reason))
		return false
	}
	if !cooldownUntil.IsZero() {
		log.Info("model cooling down for session",
			"role", role,
			"model", model,
			"reason", string(reason),
			"upstream_status", primaryStatus,
			"demotion_expires_at", cooldownUntil.UTC().Format(time.RFC3339),
		)
		return true
	}
	log.Info("model demoted for session",
		"role", role,
		"model", model,
		"reason", string(reason),
		"upstream_status", primaryStatus,
	)
	return true
}

func demotionRoles(role, pinRole string) []string {
	roles := make([]string, 0, 3)
	for _, candidate := range []string{role, pinRole, hmmHistoryRole(pinRole)} {
		if !slices.Contains(roles, candidate) {
			roles = append(roles, candidate)
		}
	}
	return roles
}

func clusterAllowlistsPinModel(overrides map[string][]string, model string) bool {
	if len(overrides) == 0 {
		return false
	}
	for _, models := range overrides {
		for _, listed := range models {
			if listed != model {
				return false
			}
		}
	}
	return true
}

func armDemotionLogFields(committedDemoted, rescuedDemoted string) []any {
	return armStrikeLogFields(committedDemoted, rescuedDemoted, sessionpin.DemotionReasonRescuedFailure)
}

func armStrikeLogFields(committedDemoted, rescuedDemoted string, rescuedReason sessionpin.DemotionReason) []any {
	return armStrikeLogFieldsWithPrimaryReason(committedDemoted, sessionpin.DemotionReasonCommittedStreamFailure, rescuedDemoted, rescuedReason)
}

func armStrikeLogFieldsWithPrimaryReason(primaryDemoted string, primaryReason sessionpin.DemotionReason, rescuedDemoted string, rescuedReason sessionpin.DemotionReason) []any {
	model, reason := primaryDemoted, ""
	if model != "" {
		reason = string(primaryReason)
	}
	if model == "" && rescuedDemoted != "" {
		if rescuedReason == "" {
			rescuedReason = sessionpin.DemotionReasonRescuedFailure
		}
		model, reason = rescuedDemoted, string(rescuedReason)
	}
	return []any{"arm_demoted", model, "arm_demotion_reason", reason, "rescued_arm_demoted", rescuedDemoted}
}

func isRescuedPrimaryFailure(err error) bool {
	if err == nil {
		return false
	}
	if providers.IsResponseHeaderTimeout(err) {
		return true
	}
	if errors.Is(err, providers.ErrUpstreamIdleTimeout) ||
		errors.Is(err, providers.ErrUpstreamOutputStall) ||
		errors.Is(err, providers.ErrUpstreamSlowThroughput) {
		return true
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if providers.IsUpstreamModelNotFound(err) || isSubscriptionPoolError(err) {
		return false
	}
	return upstreamStatus(err) != providerOverloadedStatus
}

func isCommittedStreamFailure(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	err = dispatchErrorBehindFrames(err)
	if isUpstreamWatchdogError(err) || isUpstreamWatchdogError(context.Cause(ctx)) {
		return true
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	switch status := upstreamStatus(err); {
	case status == providerOverloadedStatus:
		return false
	case status >= http.StatusInternalServerError:
		return true
	case status != 0:
		return false
	}
	return ctx.Err() == nil
}

func dispatchErrorBehindFrames(err error) error {
	for {
		var synthetic *providers.UpstreamStatusError
		if !errors.As(err, &synthetic) || synthetic.Cause == nil {
			return err
		}
		err = synthetic.Cause
	}
}

func isUpstreamWatchdogError(err error) bool {
	return errors.Is(err, providers.ErrUpstreamIdleTimeout) ||
		errors.Is(err, providers.ErrUpstreamOutputStall) ||
		errors.Is(err, providers.ErrUpstreamSlowThroughput)
}
