package proxy

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"strings"

	"weave-os/router/internal/conversation"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/sessionstrategy"
	"weave-os/router/internal/subscriptions/entitlement"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
)

const (
	betaEnabledMessage  = "Beta enabled. Type /beta again to turn it off."
	betaDisabledMessage = "Beta disabled. Stable routing restored."
	betaUsageMessage    = "Usage: /beta"
	betaUnavailable     = "Beta is unavailable for this session."
	betaPinnedMessage   = "Beta is always on for this installation; /beta has no effect."
	qwenEnabledMessage  = "Qwen beta enabled. Type /beta-llm again to turn it off."
	qwenDisabledMessage = "Qwen beta disabled. Stable routing restored."
	qwenUsageMessage    = "Usage: /beta-llm"
	qwenUnavailable     = "Qwen beta is unavailable for this session."
)

func WriteRetiredBetaRequest(w http.ResponseWriter, r *http.Request, body []byte) (bool, error) {
	var surface requestcontext.ConversationSurface
	switch r.URL.Path {
	case "/v1/messages", "/v1/route", "/v1/route/preview", "/v1/route/handoff":
		surface = requestcontext.ConversationAnthropic
	case "/v1/chat/completions":
		surface = requestcontext.ConversationChat
	case "/v1/responses":
		surface = requestcontext.ConversationResponses
	default:
		if strings.HasPrefix(r.URL.Path, "/v1beta/models/") {
			surface = requestcontext.ConversationGemini
		} else {
			return false, nil
		}
	}
	return translate.WriteRetiredBetaRequest(w, r, body, surface)
}

type betaArtifactHistoryContextKey struct{}

func withBetaArtifactHistory(ctx context.Context) context.Context {
	return context.WithValue(ctx, betaArtifactHistoryContextKey{}, true)
}

func betaArtifactHistoryFromContext(ctx context.Context) bool {
	found, _ := ctx.Value(betaArtifactHistoryContextKey{}).(bool)
	return found
}

func internalBetaSession(ctx context.Context) bool {
	identity, managed := requestcontext.ServingIdentityFromContext(ctx)
	return managed && policyregistry.ExperimentalRoutingModelsPermitted(policyregistry.ServingTarget(identity.Target), entitlement.Plan(identity.Plan)) && identity.ReleaseID != "" && identity.BindingID != "" && identity.CredentialIdentity != ""
}

func (s *Service) WithSessionStrategyStore(store sessionstrategy.Store) *Service {
	s.sessionStrategyStore = store
	return s
}

func (s *Service) applySessionStrategy(
	ctx context.Context,
	installationID uuid.UUID,
	preferenceKey [sessionpin.SessionKeyLen]byte,
) (context.Context, error) {
	if router.StrategySelectionFromContext(ctx).RoutingModelSelected() {
		return ctx, nil
	}
	if router.StrategyFromContext(ctx) == router.StrategyLLMClassifier {
		return ctx, nil
	}
	if _, managed := requestcontext.ServingIdentityFromContext(ctx); managed && !internalBetaSession(ctx) {
		return ctx, nil
	}
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
	if !router.IsBetaStrategy(preference.Strategy) {
		return ctx, fmt.Errorf("unsupported persisted session routing strategy %q", preference.Strategy)
	}
	if preference.Strategy == router.StrategyHMMQwen && !internalBetaSession(ctx) {
		return ctx, nil
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
	if router.StrategySelectionFromContext(ctx).RoutingModelSelected() {
		return writeBetaCommandResponse(w, env, fmt.Sprintf("Select a router with `wv router set-model %s`, `wv router set-model %s`, `wv router set-model %s`, `wv router set-model %s`, or `wv router set-model %s`, then start a new session.", router.ModelHMM, router.ModelHMMBeta, router.ModelQwen, router.ModelDecision, router.ModelAutomatic), inputTokens)
	}
	strategy := cmd.Strategy
	enabledMessage, disabledMessage, usageMessage, unavailableMessage := betaEnabledMessage, betaDisabledMessage, betaUsageMessage, betaUnavailable
	if strategy == router.StrategyHMMQwen {
		enabledMessage, disabledMessage, usageMessage, unavailableMessage = qwenEnabledMessage, qwenDisabledMessage, qwenUsageMessage, qwenUnavailable
		if !internalBetaSession(ctx) {
			return writeBetaCommandResponse(w, env, unavailableMessage, inputTokens)
		}
	}
	if _, managed := requestcontext.ServingIdentityFromContext(ctx); managed && !internalBetaSession(ctx) {
		return writeBetaCommandResponse(w, env, translate.BetaRetiredMessage, inputTokens)
	}
	if cmd.Invalid {
		return writeBetaCommandResponse(w, env, usageMessage, inputTokens)
	}
	if s.sessionStrategyStore == nil || installationID == uuid.Nil || preferenceKey == ([sessionpin.SessionKeyLen]byte{}) || clientSessionIDForRequest(ctx, env) == "" {
		return writeBetaCommandResponse(w, env, unavailableMessage, inputTokens)
	}
	currentStrategy := router.StrategyFromContext(ctx)
	previousStrategy := currentStrategy
	previous, found, err := s.sessionStrategyStore.Get(ctx, installationID, preferenceKey)
	if err != nil {
		return fmt.Errorf("read session routing strategy before beta toggle: %w", err)
	}
	if found {
		previousStrategy = previous.Strategy
	}
	if currentStrategy == strategy {
		return writeBetaCommandResponse(w, env, betaPinnedMessage, inputTokens)
	}

	nowEnabled := false
	available := s.PolicyStrategyAvailable(strategy)
	if snapshot := policyregistry.ServingSnapshotFromContext(ctx); snapshot != nil {
		available = snapshot.Routers[strategy] != nil
	}
	if available {
		enabled, err := s.sessionStrategyStore.Toggle(context.WithoutCancel(ctx), sessionstrategy.Preference{
			InstallationID: installationID,
			SessionKey:     preferenceKey,
			Strategy:       strategy,
		})
		if err != nil {
			return fmt.Errorf("toggle beta routing: %w", err)
		}
		nowEnabled = enabled
	} else {
		wasEnabled, err := s.sessionStrategyStore.Disable(context.WithoutCancel(ctx), installationID, preferenceKey, strategy)
		if err != nil {
			return fmt.Errorf("disable beta routing: %w", err)
		}
		if !wasEnabled {
			return writeBetaCommandResponse(w, env, unavailableMessage, inputTokens)
		}
	}

	message := enabledMessage
	if !nowEnabled {
		previousStrategy = strategy
		message = disabledMessage
	}

	log := observability.FromContext(ctx)
	if err := s.invalidateSessionRoutingState(
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
		"session_key_prefix", conversation.ShortSessionKey(sessionKey),
	)
	return writeBetaCommandResponse(w, env, message, inputTokens)
}

func (s *Service) invalidateSessionRoutingState(
	ctx context.Context,
	strategy router.Strategy,
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
	seen := make(map[string]struct{}, len(roles)*3)
	var firstErr error
	for _, role := range roles {
		for _, stateRole := range []string{
			role,
			hmmHistoryRole(role),
			conversation.CommandContinuationRole(role),
		} {
			if _, duplicate := seen[stateRole]; duplicate {
				continue
			}
			seen[stateRole] = struct{}{}
			if _, _, err := s.pinStore.Consume(context.WithoutCancel(ctx), sessionKey, stateRole, strategy); err != nil && firstErr == nil {
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
