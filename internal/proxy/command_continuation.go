package proxy

import (
	"context"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/routingpolicy"

	"github.com/google/uuid"
)

const commandContinuationRoleSuffix = "_cmd_next"

func commandContinuationRole(role string) string {
	if role == "" {
		role = sessionpin.DefaultRole
	}
	return role + commandContinuationRoleSuffix
}

func (s *Service) grantPostCommandContinuation(
	ctx context.Context,
	installationID uuid.UUID,
	sessionKey [sessionpin.SessionKeyLen]byte,
	role string,
) {
	sessionPins := routingpolicy.NewSessionPins(s.pinStore, s.clock.Now)
	if s.pinStore == nil || installationID == uuid.Nil {
		return
	}
	pin, found := sessionPins.Load(ctx, sessionKey, role)
	if !found || pin.Model == "" || pin.Provider == "" || isUserForcedReason(pin.Reason) {
		return
	}
	if maxedModel := maxedOutServedModel(pin); maxedModel != "" {
		observability.FromContext(ctx).Info(
			"skipping one-shot post-command continuation for maxed-out pin",
			"pin_model", pin.Model,
			"maxed_model", maxedModel,
			"last_output_tokens", pin.LastOutputTokens,
			"role", role,
		)
		return
	}
	pin.Role = commandContinuationRole(role)
	pin.InstallationID = installationID
	pin.Strategy = router.StrategyFromContext(ctx)
	pin.TurnCount = 1
	pin.PinnedUntil = pinExpiry(pin.Reason, s.clock.Now())
	sessionPins.Upsert(ctx, pin)
	observability.FromContext(ctx).Info("stored one-shot post-command continuation", "model", pin.Model, "provider", pin.Provider, "role", role)
}

func (s *Service) consumePostCommandContinuation(
	ctx context.Context,
	sessionKey [sessionpin.SessionKeyLen]byte,
	role string,
) (sessionpin.Pin, bool) {
	if s.pinStore == nil {
		return sessionpin.Pin{}, false
	}
	pin, found, err := s.pinStore.Consume(ctx, sessionKey, commandContinuationRole(role), router.StrategyFromContext(ctx))
	if err != nil {
		observability.FromContext(ctx).Error("post-command continuation consume failed", "err", err)
		return sessionpin.Pin{}, false
	}
	if found && !pinMatchesEffectiveStrategy(ctx, pin) {
		return sessionpin.Pin{}, false
	}
	return pin, found
}

func (s *Service) invalidatePostCommandContinuation(
	ctx context.Context,
	sessionKey [sessionpin.SessionKeyLen]byte,
	role string,
) error {
	if s.pinStore == nil {
		return nil
	}
	_, _, err := s.pinStore.Consume(context.WithoutCancel(ctx), sessionKey, commandContinuationRole(role), router.StrategyFromContext(ctx))
	if err != nil {
		observability.FromContext(ctx).Error(
			"post-command continuation invalidation failed",
			"err", err,
			"role", role,
		)
	}
	return err
}
