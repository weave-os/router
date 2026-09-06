package proxy

import (
	"context"
	"fmt"
	"net/http"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/sessionstrategy"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
)

const (
	betaEnabledMessage  = "Beta enabled. Type /beta again to turn it off."
	betaDisabledMessage = "Beta disabled. Stable routing restored."
	betaUsageMessage    = "Usage: /beta"
	betaUnavailable     = "Beta is unavailable for this session."
)

type betaArtifactHistoryContextKey struct{}

// Historical /beta control turns prove this transcript crossed a policy
// boundary even though its strategy-specific pin history was invalidated.
func withBetaArtifactHistory(ctx context.Context) context.Context {
	return context.WithValue(ctx, betaArtifactHistoryContextKey{}, true)
}

func betaArtifactHistoryFromContext(ctx context.Context) bool {
	found, _ := ctx.Value(betaArtifactHistoryContextKey{}).(bool)
	return found
}

// WithSessionStrategyStore wires durable per-session routing preferences.
// A nil store leaves normal routing unchanged and makes /beta unavailable.
func (s *Service) WithSessionStrategyStore(store sessionstrategy.Store) *Service {
	s.sessionStrategyStore = store
	return s
}

// applySessionStrategy reads the /beta preference stored under preferenceKey
// (see deriveBetaSessionKeyForRequest), not the per-thread pin key.
func (s *Service) applySessionStrategy(
	ctx context.Context,
	installationID uuid.UUID,
	preferenceKey [sessionpin.SessionKeyLen]byte,
) (context.Context, error) {
	if s.sessionStrategyStore == nil || installationID == uuid.Nil || preferenceKey == ([sessionpin.SessionKeyLen]byte{}) {
		return ctx, nil
	}
	preference, found, err := s.sessionStrategyStore.Get(ctx, installationID, preferenceKey)
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
	preferenceKey [sessionpin.SessionKeyLen]byte,
	inputTokens int,
) error {
	if cmd.Invalid {
		return writeBetaCommandResponse(w, env, betaUsageMessage, inputTokens)
	}
	if s.sessionStrategyStore == nil || installationID == uuid.Nil || preferenceKey == ([sessionpin.SessionKeyLen]byte{}) || clientSessionIDForRequest(ctx, env) == "" {
		return writeBetaCommandResponse(w, env, betaUnavailable, inputTokens)
	}

	// Both branches decide from what the store persisted rather than a prior
	// read, so overlapping /beta commands for one session cannot act on the
	// same stale state: an unavailable beta policy can only ever be left.
	nowEnabled := false
	if s.PolicyStrategyAvailable(router.StrategyHMMBeta) {
		enabled, err := s.sessionStrategyStore.Toggle(context.Background(), sessionstrategy.Preference{
			InstallationID: installationID,
			SessionKey:     preferenceKey,
			Strategy:       router.StrategyHMMBeta,
		})
		if err != nil {
			return fmt.Errorf("toggle beta routing: %w", err)
		}
		nowEnabled = enabled
	} else {
		wasEnabled, err := s.sessionStrategyStore.Disable(context.Background(), installationID, preferenceKey)
		if err != nil {
			return fmt.Errorf("disable beta routing: %w", err)
		}
		if !wasEnabled {
			return writeBetaCommandResponse(w, env, betaUnavailable, inputTokens)
		}
	}

	message := betaEnabledMessage
	previousStrategy := router.StrategyFromContext(ctx)
	if !nowEnabled {
		previousStrategy = router.StrategyHMMBeta
		message = betaDisabledMessage
	}

	log := observability.FromContext(ctx)
	// Persist first: strategy-bound reads make old-strategy pins ineligible
	// immediately, so cleanup cannot overwrite a concurrent new-mode pin.
	// Pins stay keyed per thread, so only this thread's state is dropped.
	if err := s.invalidateSessionRoutingState(
		router.WithStrategy(ctx, previousStrategy),
		sessionKey,
	); err != nil {
		log.Error("post-toggle routing-state cleanup failed", "err", err)
	}

	log.Info(
		"session beta routing toggled",
		"enabled", nowEnabled,
		"session_key_prefix", shortSessionKey(sessionKey),
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
