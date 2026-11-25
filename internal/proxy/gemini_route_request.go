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

func geminiRouteRequest(ctx context.Context, s *Service, headers http.Header, in geminiRouteInput) router.Request {
	routingPromptText, routingMessages := routingInputForRequest(ctx, in.env, in.promptText)
	return router.Request{
		RequestedModel:               in.feats.Model,
		ForceCluster:                 in.forceCluster,
		EstimatedInputTokens:         in.feats.Tokens,
		HasTools:                     in.feats.HasTools,
		HasImages:                    in.feats.HasImages,
		ImageCount:                   in.feats.ImageCount,
		TranslationRequirements:      in.env.TranslationRequirements(router.EndpointGeminiGenerate),
		ReasoningConfigurationSHA256: in.env.ReasoningConfigurationSHA256(),
		ToolConfigurationSHA256:      in.env.ToolConfigurationSHA256(),
		PromptText:                   routingPromptText,
		ConversationMessages:         routingMessages,
		AvailableTools:               availableToolsForRouting(in.env),
		Tools:                        toolsForRouting(in.env),
		ClientSessionID:              clientSessionIDForRequest(ctx, in.env),
		EnabledProviders:             in.eligibility.EnabledProviders,
		CustomBindings:               in.eligibility.CustomBindings,
		GatewayProviders:             s.gatewayProvidersForRequest(ctx),
		SubscriptionProviders:        s.selectableSubscriptionProviders(ctx, headers),
		Candidates:                   candidates.PrefilteredCandidates(in.prefiltered.Excluded, allowedModelsForRequest(ctx), in.prefiltered),
		PreferredModels:              preferredModelsForRequest(ctx),
		RoutingKnobs:                 router.RoutingKnobsFromContext(ctx),
		ClusterArmOverrides:          clusterArmOverridesForRequest(ctx),
		ProductEligibility:           entitlement.ModelBoundaryFromContext(ctx),
	}
}
