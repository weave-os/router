package proxy

import (
	"context"
	"fmt"
	"net/http"

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
	strategy router.Strategy,
	installationID uuid.UUID,
	preferenceKey [sessionpin.SessionKeyLen]byte,
) (context.Context, router.Strategy, error) {
	if router.StrategySelectionFromContext(ctx).RoutingModelSelected() {
		return ctx, strategy, nil
	}
	if router.ClassifiesThreads(strategy) {
		return ctx, strategy, nil
	}
	if _, managed := requestcontext.ServingIdentityFromContext(ctx); managed && !internalBetaSession(ctx) {
		return ctx, strategy, nil
	}
	sessionStrategies := s.lifecycle.SessionStrategies()
	if sessionStrategies == nil || installationID == uuid.Nil || preferenceKey == ([sessionpin.SessionKeyLen]byte{}) {
		return ctx, strategy, nil
	}
	preference, found, err := sessionStrategies.Get(ctx, installationID, preferenceKey)
	if err != nil {
		return ctx, strategy, fmt.Errorf("load session routing strategy: %w", err)
	}
	if !found {
		return ctx, strategy, nil
	}
	if !router.IsBetaStrategy(preference.Strategy) {
		return ctx, strategy, fmt.Errorf("unsupported persisted session routing strategy %q", preference.Strategy)
	}
	if preference.Strategy == router.StrategyHMMQwen && !internalBetaSession(ctx) {
		return ctx, strategy, nil
	}
	return router.WithStrategy(ctx, preference.Strategy), preference.Strategy, nil
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
