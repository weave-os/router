package proxy

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"
)

const providerOverloadedStatus = dispatch.ProviderOverloadedStatus

const providerOverloadStrikeThreshold = 2

func (s *Service) maybeDisableProviderAfterOverload(
	ctx context.Context,
	stickyHit bool,
	proxyErr error,
	finalProvider string,
	decisionReason string,
	target sessionTarget,
	role string,
	pinRole string,
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
		if err := s.pinStore.ResetOverloadErrors(context.WithoutCancel(ctx), target.sessionKey, role, target.strategy); err != nil {
			log.Error("pin overload-counter reset failed", "err", err, "role", role)
		}
		return
	}

	if upstreamStatus(proxyErr) != providerOverloadedStatus {
		return
	}

	count, err := s.pinStore.IncrementOverloadErrors(context.WithoutCancel(ctx), target.sessionKey, role, target.strategy)
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

	if err := s.pinStore.DisableProvider(context.WithoutCancel(ctx), target.sessionKey, role, finalProvider, target.strategy); err != nil {
		log.Error("pin provider-disable upsert failed", "err", err, "role", role, "provider", finalProvider)
		return
	}
	if pinRole == "" {
		pinRole = sessionpin.DefaultRole
	}
	if err := s.expireSessionPinAndSwitchHistory(ctx, target.strategy, target.installationID, target.sessionKey, pinRole, "provider_overloaded"); err != nil {
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
