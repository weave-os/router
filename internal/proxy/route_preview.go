package proxy

import (
	"context"
	"fmt"
	"net/http"

	"github.com/google/uuid"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"
)

func (s *Service) anthropicRoutingRequest(
	ctx context.Context,
	body []byte,
	headers http.Header,
	ingress string,
) (context.Context, router.Request, error) {
	ctx = s.withUsageObserver(ctx, headers, routePathMessages)
	log := observability.FromContext(ctx)
	cleanBody, err := stripRoutingMarkerFromMessages(body)
	if err != nil {
		return ctx, router.Request{}, fmt.Errorf("strip routing marker: %w", err)
	}
	if withoutFooter, footerErr := translate.StripFeedbackFooterFromMessages(cleanBody); footerErr != nil {
		log.Error("Failed to strip feedback footer from route preview", "err", footerErr)
	} else {
		cleanBody = withoutFooter
	}
	if canonical, _, modelErr := translate.CanonicalizeModelInBody(cleanBody); modelErr != nil {
		log.Error("Failed to canonicalize model for route preview", "err", modelErr)
	} else {
		cleanBody = canonical
	}

	env, err := translate.ParseAnthropic(cleanBody)
	if err != nil {
		return ctx, router.Request{}, fmt.Errorf("parse request: %w", err)
	}

	apiKeyID, _ := ctx.Value(APIKeyIDContextKey{}).(string)
	var sessionKey [sessionpin.SessionKeyLen]byte
	ctx, log, sessionKey = bindRequestLogger(ctx, env, apiKeyID, "", ingress)
	if removed := env.StripRouterFeedbackArtifacts(); removed > 0 {
		log.Info("Stripped router-feedback artifacts from route preview", "removed_messages", removed)
	}
	if removed := env.StripBetaArtifacts(); removed > 0 {
		log.Info("Stripped beta artifacts from route preview", "removed_messages", removed)
	}
	ctx, err = s.applySessionStrategy(ctx, installationIDFromContext(ctx), deriveBetaSessionKeyForRequest(ctx, env, apiKeyID, sessionKey))
	if err != nil {
		return ctx, router.Request{}, err
	}
	embedOnlyUser := s.ResolveEmbedOnlyUserMessage(ctx)
	features := env.RoutingFeatures(embedOnlyUser)
	promptText := features.PromptText
	if embedOnlyUser && features.OnlyUserMessageText != "" {
		promptText = features.OnlyUserMessageText
	}

	enabledProviders := s.enabledProvidersForRequest(ctx, providers.ProviderAnthropic, headers)
	if billing.SubscriptionOnlyFromContext(ctx) {
		enabledProviders = restrictToSubscriptionProviders(ctx, headers, enabledProviders)
	}
	outputReserve := contextWindowOutputReserve
	if features.MaxTokens > outputReserve {
		outputReserve = features.MaxTokens
	}
	excluded := s.excludeCodexOAuthOnlyModels(ctx, headers, enabledProviders, s.excludedModelsForRequest(ctx))
	excluded, _ = excludeContextOverflowModels(
		env.ContextOverflowTokenEstimate(),
		env.SignatureTokenSavings(),
		outputReserve,
		enabledProviders,
		excluded,
		s.availableModels,
	)
	excluded, _ = excludeGemini3xOnUnsignedHistory(env, excluded, s.availableModels)

	organizationID, _ := ctx.Value(ExternalIDContextKey{}).(string)
	installationID := ""
	if id := installationIDFromContext(ctx); id != uuid.Nil {
		installationID = id.String()
	}
	return ctx, router.Request{
		RequestedModel:               features.Model,
		EstimatedInputTokens:         features.Tokens,
		HasTools:                     features.HasTools,
		HasImages:                    features.HasImages,
		TranslationRequirements:      env.TranslationRequirements(router.EndpointAnthropicMessages),
		ReasoningConfigurationSHA256: env.ReasoningConfigurationSHA256(),
		ToolConfigurationSHA256:      env.ToolConfigurationSHA256(),
		PromptText:                   promptText,
		ConversationMessages:         conversationMessagesForRouting(env),
		AvailableTools:               availableToolsForRouting(env),
		Tools:                        toolsForRouting(env),
		OrganizationID:               organizationID,
		InstallationID:               installationID,
		ClientSessionID:              clientSessionIDForRequest(ctx, env),
		EnabledProviders:             enabledProviders,
		CustomBindings:               s.customBindingsForRequest(ctx),
		GatewayProviders:             s.gatewayProvidersForRequest(ctx),
		ExcludedModels:               excluded,
		AllowedModels:                allowedModelsForRequest(ctx),
		PreferredModels:              s.preferredModelsForRequest(ctx),
		RoutingKnobs:                 routingKnobsForRequest(ctx),
	}, nil
}

// PreviewAnthropicRoute evaluates an Anthropic request with the registered
// policy preview contract without dispatching or invoking serving lifecycle state.
func (s *Service) PreviewAnthropicRoute(ctx context.Context, body []byte, headers http.Header) (policy.PreviewResult, error) {
	ctx, req, err := s.anthropicRoutingRequest(ctx, body, headers, "anthropic_route_preview")
	if err != nil {
		return policy.PreviewResult{}, err
	}
	req, err = s.applyTranslationPlan(ctx, req)
	if err != nil {
		return policy.PreviewResult{}, err
	}
	req = s.withPolicyRequestContext(ctx, req)
	strategy := router.StrategyFromContext(ctx)
	registered, ok := s.strategies[strategy]
	if !ok || registered.router == nil {
		return policy.PreviewResult{}, fmt.Errorf("strategy %q requested but no router configured: %w", strategy, defaultStrategyUnavailable(strategy))
	}
	previewer, ok := registered.router.(policy.RoutePreviewer)
	if !ok {
		return policy.PreviewResult{}, fmt.Errorf("strategy %q has no route preview contract: %w", strategy, registered.unavailable)
	}
	return previewer.PreviewRoute(ctx, req)
}
