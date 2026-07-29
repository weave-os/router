package proxy

import (
	"context"
	"strings"

	"workweave/router/internal/observability"
	"workweave/router/internal/router/sessionpin"
	"workweave/router/internal/translate"

	"github.com/google/uuid"
)

// providerOverloadedStatus is the HTTP status Anthropic's SSE prelude
// synthesizes for an in-stream `overloaded_error` event
// (anthropic.anthropicOverloadedStatus). It is the only "provider is out of
// capacity, not just a transient 5xx" signal in this codebase today — other
// 5xx/429/408 are ordinary transient faults already handled by
// dispatchWithFallback's retry/failover loop.
const providerOverloadedStatus = 529

// providerOverloadStrikeThreshold mirrors pinEvictionStrikeThreshold: two
// consecutive turns that exhaust with a client-visible 529 on the same
// pinned provider before it's struck out for the rest of the session. One
// tolerates a single blip that clears on the next turn; a second confirms
// the provider is genuinely out of capacity right now.
const providerOverloadStrikeThreshold = 2

// maybeDisableProviderAfterOverload applies the two-strike overload policy
// for a turn run against a sticky pin: a successful turn resets the strike
// counter, a turn that exhausts with a client-visible 529 increments it, and
// hitting providerOverloadStrikeThreshold adds finalProvider to the pin's
// DisabledProviders set and evicts the pin so the next turn re-routes around
// it.
//
// finalProvider is the binding that actually served (or last attempted) this
// turn — dispatchWithFallback may have already walked away from the
// routing decision's original provider before exhausting, and it's the
// provider that just proved unreliable, not necessarily decision.Provider,
// that must be struck out.
//
// No-ops when there's no decision history yet (!stickyHit), no addressable
// pin row (zero session_key/installation_id), the pin was user-forced (user
// keeps /unforce-model as the escape hatch), or the error isn't a 529.
// Ordinary retryable 5xx/429/408 from other providers already have
// dispatchWithFallback's retry/failover and don't touch this counter — a
// generic "any 5xx trips this" policy would strike out a provider for one
// unlucky blip instead of confirmed sustained overload. Errors from the
// increment/disable/upsert are logged and swallowed since this is
// best-effort and must not change the client-visible outcome.
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
