package proxy

import (
	"context"
	"errors"
	"slices"
	"time"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"

	"github.com/google/uuid"
)

type sessionArmDemoter struct {
	s       *Service
	target  sessionTarget
	role    string
	pinRole string
}

func (d sessionArmDemoter) DemoteArm(ctx context.Context, model string, reason sessionpin.DemotionReason, cooldownUntil time.Time, upstreamStatus int) bool {
	return d.s.demoteArmForSession(ctx, model, reason, cooldownUntil, upstreamStatus, d.target, d.role, d.pinRole)
}

func sessionArmDemoterFor(s *Service, target sessionTarget, role, pinRole string) dispatch.SessionArmDemoter {
	if s.pinStore == nil || target.installationID == uuid.Nil || target.sessionKey == ([sessionpin.SessionKeyLen]byte{}) {
		return nil
	}
	return sessionArmDemoter{s: s, target: target, role: role, pinRole: pinRole}
}

func (s *Service) maybeDemoteArmAfterCommittedStreamFailure(
	ctx context.Context,
	committedStream bool,
	hardPinned bool,
	proxyErr error,
	model string,
	decisionReason string,
	target sessionTarget,
	role string,
	pinRole string,
) string {
	return s.armDemotion.AfterCommittedStreamFailure(ctx, committedStream, dispatch.ArmFailure{
		Session: sessionArmDemoterFor(s, target, role, pinRole), HardPinned: hardPinned, Err: proxyErr, Model: model, Reason: decisionReason,
	})
}

func (s *Service) maybeStrikeArmAfterRescuedFailure(
	ctx context.Context,
	rescueRan bool,
	hardPinned bool,
	primaryErr error,
	primary router.Decision,
	target sessionTarget,
	role string,
	pinRole string,
) (string, sessionpin.DemotionReason) {
	return s.armDemotion.StrikeAfterRescuedFailure(ctx, dispatch.RescuedFailureStrike{
		ArmFailure: dispatch.ArmFailure{
			Session: sessionArmDemoterFor(s, target, role, pinRole), HardPinned: hardPinned, Err: primaryErr, Model: primary.Model, Reason: primary.Reason,
		},
		RescueRan:             rescueRan,
		RescuedPrimaryFailure: isRescuedPrimaryFailure(primaryErr),
		RateLimit:             s.throttling.RateLimit,
		Now:                   s.clock.Now,
		RecordTurnFailure:     requestcontext.RecordTurnFailure,
	})
}

func (s *Service) maybeDemoteArmAfterUnrescuedStall(
	ctx context.Context,
	rescueRan, committed, hardPinned bool,
	stallErr error,
	failed router.Decision,
	target sessionTarget,
	role, pinRole string,
) string {
	return s.armDemotion.AfterUnrescuedStall(ctx, rescueRan, committed, dispatch.ArmFailure{
		Session: sessionArmDemoterFor(s, target, role, pinRole), HardPinned: hardPinned, Err: stallErr, Model: failed.Model, Reason: failed.Reason,
	})
}

func isUpstreamWatchdogError(err error) bool { return dispatch.IsUpstreamWatchdogError(err) }

func (s *Service) maybeDemoteArmAfterRescuedFailure(
	ctx context.Context,
	rescueRan bool,
	hardPinned bool,
	primaryErr error,
	primary router.Decision,
	target sessionTarget,
) string {
	model, _ := s.maybeStrikeArmAfterRescuedFailure(ctx, rescueRan, hardPinned, primaryErr, primary, target, sessionpin.DefaultRole, sessionpin.DefaultRole)
	return model
}

func (s *Service) demoteArmForSession(
	ctx context.Context,
	model string,
	reason sessionpin.DemotionReason,
	cooldownUntil time.Time,
	primaryStatus int,
	target sessionTarget,
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
	cooldownStore, hasCooldownStore := s.pinStore.(sessionpin.CooldownStore)
	if !cooldownUntil.IsZero() && !hasCooldownStore {
		log.Warn("session pin store cannot persist a cooldown, demoting for the session", "model", model, "reason", string(reason))
		cooldownUntil = time.Time{}
	}
	for _, strikeRole := range demotionRoles(role, pinRole) {
		expired := expiredSessionPin(target.installationID, target.sessionKey, strikeRole, string(reason), target.strategy, s.clock.Now())
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
	if err := postCommandContinuation(s, target.strategy).Invalidate(ctx, target.sessionKey, pinRole); err != nil {
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
