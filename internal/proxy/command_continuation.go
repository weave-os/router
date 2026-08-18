package proxy

import (
	"context"

	"workweave/router/internal/observability"
	"workweave/router/internal/router/sessionpin"

	"github.com/google/uuid"
)

// Roles live in router.session_pins.role (VARCHAR(32)); the longest normal
// role is default_high, so keep this internal suffix compact.
const commandContinuationRoleSuffix = "_cmd_next"

func commandContinuationRole(role string) string {
	if role == "" {
		role = sessionpin.DefaultRole
	}
	return role + commandContinuationRoleSuffix
}

// grantPostCommandContinuation preserves an active automatic pin for exactly
// one following normal turn. The command itself never reaches the upstream,
// so it must not provoke a new model selection for the user's continuation.
func (s *Service) grantPostCommandContinuation(
	ctx context.Context,
	installationID uuid.UUID,
	sessionKey [sessionpin.SessionKeyLen]byte,
	role string,
) {
	if s.pinStore == nil || installationID == uuid.Nil {
		return
	}
	pin, found := s.loadPin(ctx, sessionKey, role)
	if !found || pin.Model == "" || pin.Provider == "" || isUserForcedReason(pin.Reason) {
		return
	}
	pin.Role = commandContinuationRole(role)
	pin.InstallationID = installationID
	pin.TurnCount = 1
	s.upsertPin(ctx, pin)
	observability.FromContext(ctx).Info("stored one-shot post-command continuation", "model", pin.Model, "provider", pin.Provider, "role", role)
}

// consumePostCommandContinuation atomically takes the pending continuation so
// simultaneous requests cannot both serve it.
func (s *Service) consumePostCommandContinuation(
	ctx context.Context,
	sessionKey [sessionpin.SessionKeyLen]byte,
	role string,
) (sessionpin.Pin, bool) {
	if s.pinStore == nil {
		return sessionpin.Pin{}, false
	}
	pin, found, err := s.pinStore.Consume(ctx, sessionKey, commandContinuationRole(role))
	if err != nil {
		observability.FromContext(ctx).Error("post-command continuation consume failed", "err", err)
		return sessionpin.Pin{}, false
	}
	return pin, found
}
