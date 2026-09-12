package proxy

import (
	"context"
	"errors"
	"net/http"
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
// flag is off, there is no addressable pin row, the pin was user-forced, or
// the failure is not upstream-owned. Deliberately not gated on stickyHit: a
// fresh authoritative pick that dies this way must be demoted too.
func (s *Service) maybeDemoteArmAfterCommittedStreamFailure(
	ctx context.Context,
	committedStream bool,
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
	if sessionKey == ([sessionpin.SessionKeyLen]byte{}) || model == "" {
		return ""
	}
	// Prefix check covers both ReasonUserForceModel and its tier_clamp suffix.
	if strings.HasPrefix(decisionReason, translate.ReasonUserForceModel) {
		return ""
	}
	if !committedStream || !isCommittedStreamFailure(proxyErr) {
		return ""
	}

	log := observability.FromContext(ctx)
	reason := sessionpin.DemotionReasonCommittedStreamFailure

	// Expire both rows for the reason maybeDisableProviderAfterOverload does:
	// hmmStayPin treats the active pin and the HMM history row as independent
	// stay candidates. It runs first because it upserts: DemoteModel is an
	// UPDATE, and a turn that never pinned (fresh authoritative pick, swept
	// session, escalation) has no row for it to touch, so the strike would be
	// dropped exactly where one-strike matters most.
	if pinRole == "" {
		pinRole = sessionpin.DefaultRole
	}
	if role == "" {
		role = sessionpin.DefaultRole
	}
	if err := s.expireSessionPinAndHMMHistory(ctx, installationID, sessionKey, pinRole, string(reason)); err != nil {
		log.Error("pin eviction after committed stream failure failed", "err", err, "role", role, "pin_role", pinRole, "model", model)
		return ""
	}
	if err := s.pinStore.DemoteModel(context.Background(), sessionKey, role, model, reason, router.StrategyFromContext(ctx)); err != nil {
		log.Error("session model demotion failed", "err", err, "role", role, "model", model)
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

// armDemotionReason is the completion line's companion to arm_demoted: empty
// unless a model was actually withdrawn this turn.
func armDemotionReason(demotedModel string) string {
	if demotedModel == "" {
		return ""
	}
	return string(sessionpin.DemotionReasonCommittedStreamFailure)
}

// isCommittedStreamFailure reports whether err is an upstream-owned end of a
// stream rather than the client going away. The sentinel-before-cancellation
// ordering is providers.IsRetryable's: the watchdogs surface an upstream stall
// by canceling the request context, so a bare context check would read them as
// client disconnects.
//
// A 529 is excluded: an in-stream overloaded_error is provider capacity, owned
// by maybeDisableProviderAfterOverload, and demoting the model would strike
// out an arm the provider will serve again minutes later.
func isCommittedStreamFailure(err error) bool {
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
	switch status := upstreamStatus(err); {
	case status == providerOverloadedStatus:
		return false
	case status >= http.StatusInternalServerError:
		return true
	case status != 0:
		return false
	}
	// Status 0 with no cancellation in the chain is a transport-level cut of
	// an already-committed stream.
	return true
}
