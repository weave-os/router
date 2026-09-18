package proxy

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
)

// maybeDemoteArmAfterCommittedStreamFailure withdraws model from the session's
// automatic selection after its stream died with the prelude already
// committed, and expires both pin rows so the next turn re-routes. Such a turn
// can neither retry nor fail over (see preludeBuffer.Committed), so one strike
// is the whole policy: the turn is already lost, and re-picking the same arm
// loses the next one too.
//
// Returns the demoted model, or "" when nothing was demoted. No-ops when the
// flag is off, there is no addressable pin row, the turn was hard-pinned
// (utility turns and the native web-search passthrough never write session
// routing state, see recordTurnUsage), the pin was user-forced, the failure is
// not upstream-owned, or the caller's per-cluster allowlists name no other
// model (see clusterAllowlistsPinModel). Deliberately not gated on stickyHit:
// a fresh authoritative pick that dies this way must be demoted too.
//
// The strike itself is soft: it travels as AutomaticExcludedModels, and every
// enforcement site keeps the unfiltered pool when the exclusion would empty
// it. An installation whose allowlist admits only the failed model therefore
// re-picks it on the next turn; the demotion never widens the set of models
// the installation's configuration lets automatic routing choose from.
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
	// Prefix check covers both ReasonUserForceModel and its tier_clamp suffix.
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

// maybeDemoteArmAfterRescuedFailure withdraws the primary arm from the
// session's automatic selection after its attempt failed pre-commit and the
// sibling rescue ran, whether or not the rescuer then served. The rescue
// already proved the primary unusable for this turn; without the strike the
// next turn re-decides from scratch and returns to the same arm.
//
// Under transient_rate_limit a primary that failed with a buffered 429 is
// throttled, not dead: the strike is a cooldown (ResolveRateLimitCooldown)
// after which the arm is eligible again, and the reason is
// DemotionReasonRateLimited. Every other rescued failure keeps the
// session-lifetime demotion.
//
// Returns the demoted model, or "" when nothing was demoted. Shares the
// no-op conditions of maybeDemoteArmAfterCommittedStreamFailure (flag off,
// unaddressable pin row, hard-pinned or user-forced turn, allowlists that name
// no other model) and additionally leaves the arm alone when the primary's
// failure is owned by another path, see isRescuedPrimaryFailure.
func (s *Service) maybeDemoteArmAfterRescuedFailure(
	ctx context.Context,
	rescueRan bool,
	hardPinned bool,
	primaryErr error,
	primary router.Decision,
	installationID uuid.UUID,
	sessionKey [sessionpin.SessionKeyLen]byte,
	role string,
	pinRole string,
) string {
	model, _ := s.maybeStrikeArmAfterRescuedFailure(ctx, rescueRan, hardPinned, primaryErr, primary, installationID, sessionKey, role, pinRole)
	return model
}

// maybeStrikeArmAfterRescuedFailure is maybeDemoteArmAfterRescuedFailure
// reporting the strike's reason as well, for the completion line.
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
	if !s.ResolveRescuedFailureArmDemotion(ctx) || s.pinStore == nil || installationID == uuid.Nil {
		return "", ""
	}
	if sessionKey == ([sessionpin.SessionKeyLen]byte{}) || primary.Model == "" || hardPinned {
		return "", ""
	}
	if strings.HasPrefix(primary.Reason, translate.ReasonUserForceModel) {
		return "", ""
	}
	if !rescueRan || !isRescuedPrimaryFailure(primaryErr) {
		return "", ""
	}
	reason, cooldownUntil := sessionpin.DemotionReasonRescuedFailure, time.Time{}
	if s.ResolveTransientRateLimit(ctx) && isRateLimitedPrimaryFailure(primaryErr) {
		cooldown := s.ResolveRateLimitCooldown(ctx)
		reason, cooldownUntil = sessionpin.DemotionReasonRateLimited, s.clockNow().Add(cooldown)
		rateLimitTurnFromContext(ctx).recordCooldown(cooldownUntil, cooldown)
	}
	if !s.demoteArmForSession(ctx, primary.Model, reason, cooldownUntil, upstreamStatus(primaryErr), installationID, sessionKey, role, pinRole) {
		return "", ""
	}
	return primary.Model, reason
}

// demoteArmForSession writes one strike against model on every pin row the
// session's next turn merges. Reports whether the strike landed. A non-zero
// cooldownUntil records a time-limited strike instead of a session-lifetime
// one; a store without sessionpin.CooldownStore demotes for the session.
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
	// Each row is expired and struck in one strategy-guarded write. Expiring
	// covers both rows for the reason maybeDisableProviderAfterOverload does:
	// hmmStayPin treats the active pin and the HMM history row as independent
	// stay candidates. Seeding a missing row is what makes one strike hold for
	// a turn that never pinned (fresh authoritative pick, swept session,
	// escalation). The strategy guard is what keeps a late failure honest: this
	// request's pin may already have been replaced by another strategy's, and a
	// plain expiry upsert would hand that row back to us before the strike.
	//
	// The strike is written to every row the next turn merges (see
	// mergeSessionStrikes): a non-sticky HMM pick records its state only on the
	// _hmm_history row, and later HMM turns refresh that row and never the
	// expired base row, which SweepExpired would otherwise take the strike
	// down with while the session is still live.
	//
	// context.Background() for the writes: the request ctx may already be
	// canceled once the stream has ended, and the strike must still land.
	strategy := router.StrategyFromContext(ctx)
	cooldownStore, hasCooldownStore := s.pinStore.(sessionpin.CooldownStore)
	if !cooldownUntil.IsZero() && !hasCooldownStore {
		log.Warn("session pin store cannot persist a cooldown, demoting for the session", "model", model, "reason", string(reason))
		cooldownUntil = time.Time{}
	}
	for _, strikeRole := range demotionRoles(role, pinRole) {
		expired := expiredSessionPin(installationID, sessionKey, strikeRole, string(reason), strategy)
		var err error
		if cooldownUntil.IsZero() {
			err = s.pinStore.ExpireAndDemoteModel(context.Background(), expired, model, reason)
		} else {
			err = cooldownStore.ExpireAndCoolDownModel(context.Background(), expired, model, cooldownUntil, reason)
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

// demotionRoles lists the pin rows a strike is written to: the sticky-state
// role, the base pin role and its _hmm_history row, deduplicated in that order.
func demotionRoles(role, pinRole string) []string {
	roles := make([]string, 0, 3)
	for _, candidate := range []string{role, pinRole, hmmHistoryRole(pinRole)} {
		if !slices.Contains(roles, candidate) {
			roles = append(roles, candidate)
		}
	}
	return roles
}

// clusterAllowlistsPinModel reports whether the caller's per-cluster
// allowlists (org default intersected with the user's own selection) are
// configured and name no model other than model. Such a caller has pinned
// every cluster they constrained to that one model, and
// policy.ApplyClusterArmOverrides falls open to the sidecar's unconstrained
// pick when the lists admit nothing eligible; withdrawing the model would
// route them to whatever the roster has left, which may be another vendor.
// Unconfigured lists (nil or empty) never pin.
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

// armDemotionReason is the completion line's companion to arm_demoted: empty
// unless a model was actually withdrawn this turn.
func armDemotionReason(demotedModel string) string {
	if demotedModel == "" {
		return ""
	}
	return string(sessionpin.DemotionReasonCommittedStreamFailure)
}

// armDemotionLogFields is the completion line's account of the turn's strikes.
// arm_demoted and arm_demotion_reason name the one strike most turns write;
// the committed-stream strike wins when both landed (the rescuer served and
// then cut after commit), and rescued_arm_demoted always names the primary
// struck for a rescued failure so neither is lost.
func armDemotionLogFields(committedDemoted, rescuedDemoted string) []any {
	return armStrikeLogFields(committedDemoted, rescuedDemoted, sessionpin.DemotionReasonRescuedFailure)
}

// armStrikeLogFields is armDemotionLogFields with the rescued strike's own
// reason: rescued_failure for the session-lifetime strike, rate_limited for a
// cooldown.
func armStrikeLogFields(committedDemoted, rescuedDemoted string, rescuedReason sessionpin.DemotionReason) []any {
	model, reason := committedDemoted, armDemotionReason(committedDemoted)
	if model == "" && rescuedDemoted != "" {
		if rescuedReason == "" {
			rescuedReason = sessionpin.DemotionReasonRescuedFailure
		}
		model, reason = rescuedDemoted, string(rescuedReason)
	}
	return []any{"arm_demoted", model, "arm_demotion_reason", reason, "rescued_arm_demoted", rescuedDemoted}
}

// isRescuedPrimaryFailure reports whether the primary attempt's error is one
// the session should hold against the arm. The sibling rescue only runs on
// retryable, not-found, billing-blocked, pool and cross-binding failures, so
// this narrows that set to the arm-owned ones:
//
//   - a 404 is the gateway lacking the model, remembered per endpoint by
//     rememberGatewayLacksModel, so striking the arm would double-count it;
//   - a 529 is provider capacity, owned by maybeDisableProviderAfterOverload;
//   - a managed-subscription pool error has no upstream at all and is owned by
//     maybeExpireSubscriptionArmPin;
//   - a bare context cancellation is the client going away, not the arm.
//
// The upstream watchdog sentinels are checked before the cancellation check
// for the reason isCommittedStreamFailure gives.
func isRescuedPrimaryFailure(err error) bool {
	if err == nil {
		return false
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

// isCommittedStreamFailure reports whether err is an upstream-owned end of a
// stream rather than the client going away. ctx is the inbound request
// context: the server cancels it when the client disconnects, which is the
// only signal a downstream write failure carries, since httputil.StreamBody
// returns a broken pipe or connection reset from the response writer as-is,
// with no cancellation in its chain and no upstream status.
//
// The sentinel-before-cancellation ordering is providers.IsRetryable's: the
// watchdogs surface an upstream stall by canceling the dispatch context (and
// the sentinel is that cancellation's cause), so a bare context check would
// read them as client disconnects.
//
// A 529 is excluded: an in-stream overloaded_error is provider capacity, owned
// by maybeDisableProviderAfterOverload, and demoting the model would strike
// out an arm the provider will serve again minutes later.
//
// Post-commit, the SSE error frame renderers hand back a synthetic
// *UpstreamStatusError whose status was chosen for the wire, not read from the
// upstream; the dispatch error it stands in for is what gets classified.
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
	// Status 0 with no cancellation in the chain: a transport-level cut of an
	// already-committed stream when the client is still there, otherwise the
	// client's own disconnect surfacing as a downstream write error.
	return ctx.Err() == nil
}

// dispatchErrorBehindFrames strips every synthetic SSE-frame status wrapping
// err. The Anthropic path can frame twice — once in the committed attempt and
// again in the deferred flush when a declined rescue owns it — so one peel
// would still leave a synthetic 5xx in front of the client's disconnect. A
// status read off a real upstream response carries no Cause and is kept.
func dispatchErrorBehindFrames(err error) error {
	for {
		var synthetic *providers.UpstreamStatusError
		if !errors.As(err, &synthetic) || synthetic.Cause == nil {
			return err
		}
		err = synthetic.Cause
	}
}

// isUpstreamWatchdogError reports whether err carries one of the upstream
// stream watchdog sentinels (idle, output stall, slow throughput).
func isUpstreamWatchdogError(err error) bool {
	return errors.Is(err, providers.ErrUpstreamIdleTimeout) ||
		errors.Is(err, providers.ErrUpstreamOutputStall) ||
		errors.Is(err, providers.ErrUpstreamSlowThroughput)
}
