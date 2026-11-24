package proxy

import (
	"context"
	"fmt"
	"strings"
	"weave-os/router/internal/routingpolicy"

	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
)

const ReasonAgentShadowEval = "agent_shadow_eval_force"

func (s *Service) runAgentShadowEvaluationRoute(
	ctx context.Context,
	env *translate.RequestEnvelope,
	feats translate.RoutingFeatures,
	installationID uuid.UUID,
	req router.Request,
	evaluation requestcontext.AgentShadowEvaluation,
) (routingpolicy.TurnRoute, error) {
	req.TranslationRequirements = env.TranslationRequirements(router.EndpointAnthropicMessages)
	var err error
	req, err = s.applyTranslationPlan(ctx, req)
	if err != nil {
		return routingpolicy.TurnRoute{}, err
	}
	rawModel := strings.TrimSpace(evaluation.Model)
	model, provider, known, _ := resolveForceModelWithEffort(rawModel)
	if !known || model != strings.ToLower(rawModel) {
		return routingpolicy.TurnRoute{}, fmt.Errorf("agent-shadow model must be a canonical catalog id: %q", rawModel)
	}
	if err := catalog.CheckEligibility(req.ProductEligibility, model); err != nil {
		return routingpolicy.TurnRoute{}, fmt.Errorf("agent-shadow planned model %q: %w", model, err)
	}
	if _, excluded := req.Candidates.ExcludedModels[model]; excluded {
		return routingpolicy.TurnRoute{}, fmt.Errorf("agent-shadow planned model %q is no longer eligible: %w", model, router.ErrNoEligibleProvider)
	}
	if req.EnabledProviders != nil {
		if _, enabled := req.EnabledProviders[provider]; !enabled {
			provider = ""
			for _, binding := range catalog.AvailableBindings(model, req.EnabledProviders) {
				if _, excludedProvider := s.excludedProvidersForRequest(ctx)[binding.Provider]; !excludedProvider {
					provider = binding.Provider
					break
				}
			}
			if provider == "" {
				return routingpolicy.TurnRoute{}, fmt.Errorf("agent-shadow model %q has no enabled provider: %w", model, router.ErrNoEligibleProvider)
			}
		}
	}
	if _, err := s.provider(provider); err != nil {
		return routingpolicy.TurnRoute{}, fmt.Errorf("agent-shadow provider %q is unavailable: %w", provider, err)
	}
	decision := router.Decision{Model: model, Provider: provider, Reason: ReasonAgentShadowEval}
	requestedTier := catalog.TierFor(feats.Model)
	return routingpolicy.TurnRoute{
		InstallationID:      installationID,
		TurnType:            turntype.DetectFromEnvelope(env, feats, ""),
		PinTier:             "agent_shadow_eval",
		PinRole:             roleForTier(requestedTier),
		RequestedTier:       requestedTier,
		Decision:            decision,
		Fresh:               decision,
		StripThinkingBlocks: feats.Model != model || env.SignatureTokenSavings() > 0,
	}, nil
}
