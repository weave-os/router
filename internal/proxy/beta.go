package proxy

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"weave-os/router/internal/conversation"
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
	if s.lifecycle == nil {
		s.lifecycle = &conversation.Lifecycle{}
	}
	s.lifecycle.SetSessionStrategies(store)
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
	sessionStrategies := s.lifecycle.SessionStrategies()
	if sessionStrategies == nil || installationID == uuid.Nil || preferenceKey == ([sessionpin.SessionKeyLen]byte{}) {
		return ctx, nil
	}
	preference, found, err := sessionStrategies.Get(ctx, installationID, preferenceKey)
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

type betaCommands struct {
	s *Service
}

func (b betaCommands) handle(
	ctx context.Context,
	w http.ResponseWriter,
	env *translate.RequestEnvelope,
	cmd translate.BetaCommandResult,
	installationID uuid.UUID,
	sessionKey [sessionpin.SessionKeyLen]byte,
	preferenceKey [sessionpin.SessionKeyLen]byte,
	inputTokens int,
) error {
	message, err := conversation.BetaCommand{
		Strategies: b.s.lifecycle.SessionStrategies(),
		Policy:     b,
		Routing:    b,
	}.Run(ctx, conversation.BetaRequest{
		Command:         cmd,
		InstallationID:  installationID,
		SessionKey:      sessionKey,
		PreferenceKey:   preferenceKey,
		ClientSessionID: clientSessionIDForRequest(ctx, env),
	})
	if err != nil {
		return err
	}
	return writeBetaCommandResponse(w, env, message, inputTokens)
}

func (betaCommands) CurrentStrategy(ctx context.Context) router.Strategy {
	return router.StrategyFromContext(ctx)
}

func (betaCommands) InternalOnly(strategy router.Strategy) bool {
	return strategy == router.StrategyHMMQwen
}

func (betaCommands) InternalSession(ctx context.Context) bool { return internalBetaSession(ctx) }

func (betaCommands) Managed(ctx context.Context) bool {
	_, managed := requestcontext.ServingIdentityFromContext(ctx)
	return managed
}

func (b betaCommands) Available(ctx context.Context, strategy router.Strategy) bool {
	available := b.s.PolicyStrategyAvailable(strategy)
	if snapshot := policyregistry.ServingSnapshotFromContext(ctx); snapshot != nil {
		available = snapshot.Routers[strategy] != nil
	}
	return available
}

func (b betaCommands) Invalidate(
	ctx context.Context,
	strategy router.Strategy,
	sessionKey [sessionpin.SessionKeyLen]byte,
) error {
	if b.s.pinStore == nil {
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
			if _, _, err := b.s.pinStore.Consume(context.WithoutCancel(ctx), sessionKey, stateRole, strategy); err != nil && firstErr == nil {
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
