package proxy

import (
	"context"
	"fmt"
	"net/http"

	"workweave/router/internal/observability"
	"workweave/router/internal/router"
	"workweave/router/internal/router/catalog"
	"workweave/router/internal/router/sessionpin"
	"workweave/router/internal/router/sessionstrategy"
	"workweave/router/internal/translate"

	"github.com/google/uuid"
)

const (
	betaEnabledMessage  = "Beta enabled. Type /beta again to turn it off."
	betaDisabledMessage = "Beta disabled. Stable routing restored."
	betaUsageMessage    = "Usage: /beta"
	betaUnavailable     = "Beta is unavailable for this session."
)

// WithSessionStrategyStore wires durable per-session routing preferences.
// A nil store leaves normal routing unchanged and makes /beta unavailable.
func (s *Service) WithSessionStrategyStore(store sessionstrategy.Store) *Service {
	s.sessionStrategyStore = store
	return s
}

func (s *Service) applySessionStrategy(
	ctx context.Context,
	installationID uuid.UUID,
	sessionKey [sessionpin.SessionKeyLen]byte,
) (context.Context, error) {
	if s.sessionStrategyStore == nil || installationID == uuid.Nil || sessionKey == ([sessionpin.SessionKeyLen]byte{}) {
		return ctx, nil
	}
	preference, found, err := s.sessionStrategyStore.Get(ctx, installationID, sessionKey)
	if err != nil {
		return ctx, fmt.Errorf("load session routing strategy: %w", err)
	}
	if !found {
		return ctx, nil
	}
	if preference.Strategy != router.StrategyHMMBeta {
		return ctx, fmt.Errorf("unsupported persisted session routing strategy %q", preference.Strategy)
	}
	return router.WithStrategy(ctx, preference.Strategy), nil
}

func (s *Service) handleBetaCommand(
	ctx context.Context,
	w http.ResponseWriter,
	env *translate.RequestEnvelope,
	cmd translate.BetaCommandResult,
	installationID uuid.UUID,
	sessionKey [sessionpin.SessionKeyLen]byte,
	inputTokens int,
) error {
	if cmd.Invalid {
		return writeBetaCommandResponse(w, env, betaUsageMessage, inputTokens)
	}
	if s.sessionStrategyStore == nil || installationID == uuid.Nil || sessionKey == ([sessionpin.SessionKeyLen]byte{}) || env.ClientSessionID() == "" {
		return writeBetaCommandResponse(w, env, betaUnavailable, inputTokens)
	}

	preference, enabled, err := s.sessionStrategyStore.Get(ctx, installationID, sessionKey)
	if err != nil {
		return fmt.Errorf("read beta routing preference: %w", err)
	}
	if enabled && preference.Strategy != router.StrategyHMMBeta {
		return fmt.Errorf("unsupported persisted session routing strategy %q", preference.Strategy)
	}
	if !enabled && !s.PolicyStrategyAvailable(router.StrategyHMMBeta) {
		return writeBetaCommandResponse(w, env, betaUnavailable, inputTokens)
	}

	message := betaEnabledMessage
	previousStrategy := router.StrategyFromContext(ctx)
	if enabled {
		if err := s.sessionStrategyStore.Clear(context.Background(), installationID, sessionKey); err != nil {
			return fmt.Errorf("disable beta routing: %w", err)
		}
		previousStrategy = router.StrategyHMMBeta
		message = betaDisabledMessage
	} else {
		if err := s.sessionStrategyStore.Set(context.Background(), sessionstrategy.Preference{
			InstallationID: installationID,
			SessionKey:     sessionKey,
			Strategy:       router.StrategyHMMBeta,
		}); err != nil {
			return fmt.Errorf("enable beta routing: %w", err)
		}
	}

	log := observability.FromContext(ctx)
	// Persist the mode first. Strategy-bound pin reads make every old-strategy
	// row unusable immediately; cleanup then conditionally consumes only rows
	// from that old strategy, so it cannot overwrite a concurrent new-mode pin.
	if err := s.invalidateSessionRoutingState(
		router.WithStrategy(ctx, previousStrategy),
		sessionKey,
	); err != nil {
		log.Error("post-toggle routing-state cleanup failed", "err", err)
	}

	log.Info(
		"session beta routing toggled",
		"enabled", !enabled,
		"session_key", shortSessionKey(sessionKey),
	)
	return writeBetaCommandResponse(w, env, message, inputTokens)
}

func (s *Service) invalidateSessionRoutingState(
	ctx context.Context,
	sessionKey [sessionpin.SessionKeyLen]byte,
) error {
	if s.pinStore == nil {
		return nil
	}
	roles := []string{
		roleForTier(catalog.TierUnknown),
		roleForTier(catalog.TierLow),
		roleForTier(catalog.TierMid),
		roleForTier(catalog.TierHigh),
	}
	strategy := router.StrategyFromContext(ctx)
	seen := make(map[string]struct{}, len(roles)*3)
	var firstErr error
	for _, role := range roles {
		for _, stateRole := range []string{
			role,
			hmmHistoryRole(role),
			commandContinuationRole(role),
		} {
			if _, duplicate := seen[stateRole]; duplicate {
				continue
			}
			seen[stateRole] = struct{}{}
			if _, _, err := s.pinStore.Consume(context.Background(), sessionKey, stateRole, strategy); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

func writeBetaCommandResponse(
	w http.ResponseWriter,
	env *translate.RequestEnvelope,
	message string,
	inputTokens int,
) error {
	text := "✦ **Weave Router** → " + message + "\n\n"
	if env.SourceFormat() == translate.FormatOpenAI {
		return writeSyntheticOpenAIResponse(w, env, text, inputTokens)
	}
	return writeSyntheticAnthropicResponse(w, env, text, inputTokens)
}
