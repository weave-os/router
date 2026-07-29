package proxy

import (
	"context"
	"strings"

	"workweave/router/internal/observability"
	"workweave/router/internal/router/sessionpin"
	"workweave/router/internal/translate"

	"github.com/google/uuid"
)

// providerOverloadedStatus is the HTTP status Anthropic synthesizes for an
// in-stream overloaded_error; distinct from ordinary 5xx/429 transient faults.
const providerOverloadedStatus = 529

// providerOverloadStrikeThreshold mirrors pinEvictionStrikeThreshold: two
// consecutive 529-exhausted turns before the provider is struck out for the session.
const providerOverloadStrikeThreshold = 2

// maybeDisableProviderAfterOverload applies the two-strike overload policy for
// a sticky-pin turn: success resets the counter; a 529 exhaustion increments it;
// hitting providerOverloadStrikeThreshold appends finalProvider to
// DisabledProviders and evicts the pin so the next turn re-routes around it.
// No-ops when !stickyHit, zero session key, uuid.Nil installation, user-forced
// pin, or non-529 error.
func (s *Service) maybeDisableProviderAfterOverload(
	ctx context.Context,
	stickyHit bool,
	proxyErr error,
	finalProvider string,
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
		if err := s.pinStore.ResetOverloadErrors(context.Background(), sessionKey, role); err != nil {
			log.Error("pin overload-counter reset failed", "err", err, "role", role)
		}
		return
	}

	if upstreamStatus(proxyErr) != providerOverloadedStatus {
		return
	}

	count, err := s.pinStore.IncrementOverloadErrors(context.Background(), sessionKey, role)
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

	if err := s.pinStore.DisableProvider(context.Background(), sessionKey, role, finalProvider); err != nil {
		log.Error("pin provider-disable upsert failed", "err", err, "role", role, "provider", finalProvider)
		return
	}
	// Expire via a PinnedUntil in the past, same pattern as loop-break /
	// no-progress / force-model, so loadPin discards it next turn and the
	// scorer re-routes with the newly-disabled provider excluded.
	if err := s.expireSessionPin(ctx, installationID, sessionKey, role, "provider_overloaded"); err != nil {
		log.Error("pin eviction after provider overload failed", "err", err, "role", role, "provider", finalProvider)
		return
	}
	log.Info("provider disabled for session after consecutive overload errors",
		"role", role,
		"provider", finalProvider,
		"consecutive_overload_errors", count,
		"strike_threshold", providerOverloadStrikeThreshold,
	)
}
