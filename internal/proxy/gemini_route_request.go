package proxy

import (
	"context"
	"net/http"

	"weave-os/router/internal/credentials"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/candidates"
	"weave-os/router/internal/subscriptions/entitlement"
	"weave-os/router/internal/translate"
)

type geminiRouteInput struct {
	env          *translate.RequestEnvelope
	feats        translate.RoutingFeatures
	forceCluster string
	promptText   string
	eligibility  credentials.Eligibility
	prefiltered  candidates.Prefiltered
}

func geminiRouteRequest(ctx context.Context, s *Service, headers http.Header, input geminiRouteInput) router.Request {
	routingPromptText, routingMessages := routingInputForRequest(ctx, input.env, input.promptText)
	return router.Request{
		RequestedModel:               input.feats.Model,
		ForceCluster:                 input.forceCluster,
		EstimatedInputTokens:         input.feats.Tokens,
		HasTools:                     input.feats.HasTools,
		HasImages:                    input.feats.HasImages,
		ImageCount:                   input.feats.ImageCount,
		TranslationRequirements:      input.env.TranslationRequirements(router.EndpointGeminiGenerate),
		ReasoningConfigurationSHA256: input.env.ReasoningConfigurationSHA256(),
		ToolConfigurationSHA256:      input.env.ToolConfigurationSHA256(),
		PromptText:                   routingPromptText,
		ConversationMessages:         routingMessages,
		AvailableTools:               availableToolsForRouting(input.env),
		Tools:                        toolsForRouting(input.env),
		ClientSessionID:              clientSessionIDForRequest(ctx, input.env),
		EnabledProviders:             input.eligibility.EnabledProviders,
		CustomBindings:               input.eligibility.CustomBindings,
		GatewayProviders:             s.gatewayProvidersForRequest(ctx),
		SubscriptionProviders:        s.selectableSubscriptionProviders(ctx, headers),
		Candidates:                   candidates.PrefilteredCandidates(input.prefiltered.Excluded, allowedModelsForRequest(ctx), input.prefiltered),
		PreferredModels:              preferredModelsForRequest(ctx),
		RoutingKnobs:                 router.RoutingKnobsFromContext(ctx),
		ClusterArmOverrides:          clusterArmOverridesForRequest(ctx),
		ProductEligibility:           entitlement.ModelBoundaryFromContext(ctx),
	}
}
