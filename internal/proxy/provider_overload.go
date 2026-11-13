package proxy

import (
	"context"
	"strings"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
)

// providerOverloadedStatus is the HTTP status Anthropic synthesizes for an
// in-stream overloaded_error; distinct from ordinary 5xx/429 transient faults.
const providerOverloadedStatus = 529

// providerOverloadStrikeThreshold mirrors pinEvictionStrikeThreshold: two
// consecutive 529-exhausted turns before the provider is struck out for the session.
const providerOverloadStrikeThreshold = 2

// maybeDisableProviderAfterOverload applies the two-strike overload policy:
// success resets the counter; a 529 exhaustion increments it; hitting the
// threshold disables finalProvider for the session and evicts both pin rows.
// No-ops when !stickyHit, zero session key, uuid.Nil installation,
// user-forced pin, or non-529 error. role is stickyStateRole; pinRole is
// the base role needed by expireSessionPinAndHMMHistory.
func (s *Service) maybeDisableProviderAfterOverload(
	ctx context.Context,
	stickyHit bool,
	proxyErr error,
	finalProvider string,
	decisionReason string,
	installationID uuid.UUID,
	sessionKey [sessionpin.SessionKeyLen]byte,
	role string,
	pinRole string,
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
		if err := s.pinStore.ResetOverloadErrors(context.Background(), sessionKey, role, router.StrategyFromContext(ctx)); err != nil {
			log.Error("pin overload-counter reset failed", "err", err, "role", role)
		}
		return
	}

	if upstreamStatus(proxyErr) != providerOverloadedStatus {
		return
	}

	count, err := s.pinStore.IncrementOverloadErrors(context.Background(), sessionKey, role, router.StrategyFromContext(ctx))
	if err != nil {
		log.Error("pin overload-counter increment failed", "err", err, "role", role, "provider", finalProvider)
		return
	}
	if count < providerOverloadStrikeThreshold {
		log.Debug("pin overload-counter incremented",
			"role", role,
			"provider", finalProvider,
			"consecutive_overload_errors", count,
			"strike_threshold", providerOverloadStrikeThreshold,
		)
		return
	}

	if err := s.pinStore.DisableProvider(context.Background(), sessionKey, role, finalProvider, router.StrategyFromContext(ctx)); err != nil {
		log.Error("pin provider-disable upsert failed", "err", err, "role", role, "provider", finalProvider)
		return
	}
	// Expire both rows: hmmStayPin considers activePin and hmmHistory as stay
	// candidates, so a surviving active-pin row lets the overloaded provider slip through.
	if pinRole == "" {
		pinRole = sessionpin.DefaultRole
	}
	if err := s.expireSessionPinAndHMMHistory(ctx, installationID, sessionKey, pinRole, "provider_overloaded"); err != nil {
		log.Error("pin eviction after provider overload failed", "err", err, "role", role, "pin_role", pinRole, "provider", finalProvider)
		return
	}
	log.Info("provider disabled for session after consecutive overload errors",
		"role", role,
		"provider", finalProvider,
		"consecutive_overload_errors", count,
		"strike_threshold", providerOverloadStrikeThreshold,
	)
}
