package proxy

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"

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

	log := observability.FromContext(ctx)
	if clusterAllowlistsPinModel(clusterArmOverridesForRequest(ctx), model) {
		log.Info("model kept for session despite committed stream failure: cluster allowlists name no other model",
			"role", role,
			"model", model,
			"upstream_status", upstreamStatus(proxyErr),
		)
		return ""
	}
	reason := sessionpin.DemotionReasonCommittedStreamFailure

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
	for _, strikeRole := range demotionRoles(role, pinRole) {
		expired := expiredSessionPin(installationID, sessionKey, strikeRole, string(reason), strategy)
		if err := s.pinStore.ExpireAndDemoteModel(context.Background(), expired, model, reason); err != nil {
			log.Error("session model demotion failed", "err", err, "role", strikeRole, "model", model, "reason", string(reason))
			return ""
		}
	}
	if err := s.invalidatePostCommandContinuation(ctx, sessionKey, pinRole); err != nil {
		log.Error("continuation invalidation after session model demotion failed", "err", err, "role", role, "pin_role", pinRole, "model", model)
		return ""
	}
	log.Info("model demoted for session after committed stream failure",
		"role", role,
		"model", model,
		"reason", string(reason),
		"upstream_status", upstreamStatus(proxyErr),
	)
	return model
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
func isCommittedStreamFailure(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
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

// isUpstreamWatchdogError reports whether err carries one of the upstream
// stream watchdog sentinels (idle, output stall, slow throughput).
func isUpstreamWatchdogError(err error) bool {
	return errors.Is(err, providers.ErrUpstreamIdleTimeout) ||
		errors.Is(err, providers.ErrUpstreamOutputStall) ||
		errors.Is(err, providers.ErrUpstreamSlowThroughput)
}
