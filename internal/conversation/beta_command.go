package conversation

import (
	"cmp"
	"context"
	"fmt"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/sessionstrategy"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
)

const (
	BetaEnabledMessage  = "Beta enabled. Type /beta again to turn it off."
	BetaDisabledMessage = "Beta disabled. Stable routing restored."
	BetaUsageMessage    = "Usage: /beta"
	BetaUnavailable     = "Beta is unavailable for this session."
	BetaPinnedMessage   = "Beta is always on for this installation; /beta has no effect."
	QwenEnabledMessage  = "Qwen beta enabled. Type /beta-llm again to turn it off."
	QwenDisabledMessage = "Qwen beta disabled. Stable routing restored."
	QwenUsageMessage    = "Usage: /beta-llm"
	QwenUnavailable     = "Qwen beta is unavailable for this session."
)

type BetaPolicy interface {
	CurrentStrategy(ctx context.Context) router.Strategy
	InternalOnly(strategy router.Strategy) bool
	InternalSession(ctx context.Context) bool
	Managed(ctx context.Context) bool
	Available(ctx context.Context, strategy router.Strategy) bool
}

type BetaRoutingState interface {
	Invalidate(ctx context.Context, strategy router.Strategy, sessionKey [sessionpin.SessionKeyLen]byte) error
}

type BetaCommand struct {
	Strategies sessionstrategy.Store
	Policy     BetaPolicy
	Routing    BetaRoutingState
}

type BetaRequest struct {
	Command         translate.BetaCommandResult
	InstallationID  uuid.UUID
	SessionKey      [sessionpin.SessionKeyLen]byte
	PreferenceKey   [sessionpin.SessionKeyLen]byte
	ClientSessionID string
}

func (c BetaCommand) Run(ctx context.Context, req BetaRequest) (string, error) {
	if router.StrategySelectionFromContext(ctx).RoutingModelSelected() {
		return fmt.Sprintf("Select a router with `wv router set-model %s`, `wv router set-model %s`, `wv router set-model %s`, `wv router set-model %s`, or `wv router set-model %s`, then start a new session.", router.ModelHMM, router.ModelHMMBeta, router.ModelQwen, router.ModelDecision, router.ModelAutomatic), nil
	}
	cmd, installationID, sessionKey, preferenceKey := req.Command, req.InstallationID, req.SessionKey, req.PreferenceKey
	strategy := cmd.Strategy
	enabledMessage, disabledMessage, usageMessage, unavailableMessage := BetaEnabledMessage, BetaDisabledMessage, BetaUsageMessage, BetaUnavailable
	if c.Policy.InternalOnly(strategy) {
		enabledMessage, disabledMessage, usageMessage, unavailableMessage = QwenEnabledMessage, QwenDisabledMessage, QwenUsageMessage, QwenUnavailable
		if !c.Policy.InternalSession(ctx) {
			return unavailableMessage, nil
		}
	}
	if c.Policy.Managed(ctx) && !c.Policy.InternalSession(ctx) {
		return translate.BetaRetiredMessage, nil
	}
	if cmd.Invalid {
		return usageMessage, nil
	}
	if c.Strategies == nil || installationID == uuid.Nil || preferenceKey == ([sessionpin.SessionKeyLen]byte{}) || req.ClientSessionID == "" {
		return unavailableMessage, nil
	}
	currentStrategy := c.Policy.CurrentStrategy(ctx)
	previousStrategy := currentStrategy
	previous, found, err := c.Strategies.Get(ctx, installationID, preferenceKey)
	if err != nil {
		return "", fmt.Errorf("read session routing strategy before beta toggle: %w", err)
	}
	if found {
		previousStrategy = previous.Strategy
	}
	if currentStrategy == strategy {
		return BetaPinnedMessage, nil
	}

	nowEnabled := false
	if c.Policy.Available(ctx, strategy) {
		enabled, err := c.Strategies.Toggle(context.WithoutCancel(ctx), sessionstrategy.Preference{
			InstallationID: installationID,
			SessionKey:     preferenceKey,
			Strategy:       strategy,
		})
		if err != nil {
			return "", fmt.Errorf("toggle beta routing: %w", err)
		}
		nowEnabled = enabled
	} else {
		wasEnabled, err := c.Strategies.Disable(context.WithoutCancel(ctx), installationID, preferenceKey, strategy)
		if err != nil {
			return "", fmt.Errorf("disable beta routing: %w", err)
		}
		if !wasEnabled {
			return unavailableMessage, nil
		}
	}

	message := enabledMessage
	if !nowEnabled {
		previousStrategy = strategy
		message = disabledMessage
	}

	log := observability.FromContext(ctx)
	if err := c.Routing.Invalidate(
		ctx,
		cmp.Or(previousStrategy, currentStrategy),
		sessionKey,
	); err != nil {
		log.Error("post-toggle routing-state cleanup failed", "err", err)
	}

	log.Info(
		"session beta routing toggled",
		"strategy", strategy,
		"enabled", nowEnabled,
		"session_key_prefix", ShortSessionKey(sessionKey),
	)
	return message, nil
}
