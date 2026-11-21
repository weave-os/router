package conversation

import (
	"context"
	"time"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/candidates"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/routingpolicy"

	"github.com/google/uuid"
)

type ContinuationPins interface {
	Load(ctx context.Context, sessionKey [sessionpin.SessionKeyLen]byte, role string) (sessionpin.Pin, bool)
	Upsert(ctx context.Context, pin sessionpin.Pin)
	Consume(ctx context.Context, sessionKey [sessionpin.SessionKeyLen]byte, role string, strategy router.Strategy) (sessionpin.Pin, bool, error)
}

type PostCommandContinuation struct {
	Pins     ContinuationPins
	Strategy router.Strategy
	Now      func() time.Time
}

func (c PostCommandContinuation) Grant(
	ctx context.Context,
	installationID uuid.UUID,
	sessionKey [sessionpin.SessionKeyLen]byte,
	role string,
) {
	if c.Pins == nil || installationID == uuid.Nil {
		return
	}
	pin, found := c.Pins.Load(ctx, sessionKey, role)
	if !found || pin.Model == "" || pin.Provider == "" || routingpolicy.IsUserForcedReason(pin.Reason) {
		return
	}
	if maxedModel := candidates.MaxedOutServedModel(pin); maxedModel != "" {
		observability.FromContext(ctx).Info(
			"skipping one-shot post-command continuation for maxed-out pin",
			"pin_model", pin.Model,
			"maxed_model", maxedModel,
			"last_output_tokens", pin.LastOutputTokens,
			"role", role,
		)
		return
	}
	pin.Role = CommandContinuationRole(role)
	pin.InstallationID = installationID
	pin.Strategy = c.Strategy
	pin.TurnCount = 1
	pin.PinnedUntil = routingpolicy.PinExpiry(pin.Reason, c.Now())
	c.Pins.Upsert(ctx, pin)
	observability.FromContext(ctx).Info("stored one-shot post-command continuation", "model", pin.Model, "provider", pin.Provider, "role", role)
}

func (c PostCommandContinuation) Consume(
	ctx context.Context,
	sessionKey [sessionpin.SessionKeyLen]byte,
	role string,
) (sessionpin.Pin, bool) {
	if c.Pins == nil {
		return sessionpin.Pin{}, false
	}
	pin, found, err := c.Pins.Consume(ctx, sessionKey, CommandContinuationRole(role), c.Strategy)
	if err != nil {
		observability.FromContext(ctx).Error("post-command continuation consume failed", "err", err)
		return sessionpin.Pin{}, false
	}
	if found && !routingpolicy.PinMatchesEffectiveStrategy(c.Strategy, pin) {
		return sessionpin.Pin{}, false
	}
	return pin, found
}

func (c PostCommandContinuation) Invalidate(
	ctx context.Context,
	sessionKey [sessionpin.SessionKeyLen]byte,
	role string,
) error {
	if c.Pins == nil {
		return nil
	}
	_, _, err := c.Pins.Consume(context.WithoutCancel(ctx), sessionKey, CommandContinuationRole(role), c.Strategy)
	if err != nil {
		observability.FromContext(ctx).Error(
			"post-command continuation invalidation failed",
			"err", err,
			"role", role,
		)
	}
	return err
}
