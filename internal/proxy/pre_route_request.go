package proxy

import (
	"context"
	"log/slog"
	"net/http"

	"weave-os/router/internal/credentials"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/candidates"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/subscriptions/entitlement"
	"weave-os/router/internal/translate"
)

type turnPrefilterInput struct {
	env                      *translate.RequestEnvelope
	feats                    translate.RoutingFeatures
	eligibility              credentials.Eligibility
	header                   http.Header
	excludeAdvisorOutranking bool
	log                      *slog.Logger
}

func prefilterTurn(ctx context.Context, s *Service, in turnPrefilterInput) (int, candidates.Prefiltered) {
	outputReserve := max(contextWindowOutputReserve, in.feats.MaxTokens)
	baseExcluded := s.excludeCodexOAuthOnlyModels(ctx, in.header, in.eligibility.EnabledProviders, s.excludedModelsForRequest(ctx))
	prefiltered := candidates.Prefilter(candidates.PrefilterRequest{
		Envelope: in.env, ImageCount: in.feats.ImageCount, OutputReserve: outputReserve, Eligibility: in.eligibility,
		Excluded: baseExcluded, Available: s.routing.AvailableModels(),
		AdvisorExclusions: advisorExclusions(in.env, s.routableUniverse()), ExcludeAdvisorOutranking: in.excludeAdvisorOutranking,
	})
	logPrefilter(in.log, in.env, in.feats.ImageCount, outputReserve, prefiltered)
	return outputReserve, prefiltered
}

type preRouteRequestInput struct {
	env                 *translate.RequestEnvelope
	feats               translate.RoutingFeatures
	forceModel          string
	forceCluster        string
	endpoint            router.TranslationEndpoint
	reasoningConfigHash string
	toolConfigHash      string
	promptText          string
	messages            []router.ConversationMessage
	feedbackKey         string
	eligibility         credentials.Eligibility
	gatewayProviders    map[string]struct{}
	header              http.Header
	prefiltered         candidates.Prefiltered
}

func preRouteRequest(ctx context.Context, s *Service, in preRouteRequestInput) router.Request {
	return router.Request{
		RequestedModel:               in.feats.Model,
		ForceModel:                   in.forceModel,
		ForceCluster:                 in.forceCluster,
		EstimatedInputTokens:         in.feats.Tokens,
		HasTools:                     in.feats.HasTools,
		HasImages:                    in.feats.HasImages,
		ImageCount:                   in.feats.ImageCount,
		TranslationRequirements:      in.env.TranslationRequirements(in.endpoint),
		ReasoningConfigurationSHA256: in.reasoningConfigHash,
		ToolConfigurationSHA256:      in.toolConfigHash,
		PromptText:                   in.promptText,
		ConversationMessages:         in.messages,
		AvailableTools:               availableToolsForRouting(in.env),
		Tools:                        toolsForRouting(in.env),
		FeedbackKey:                  in.feedbackKey,
		FeedbackRole:                 roleForTier(catalog.TierFor(in.feats.Model)),
		ClientSessionID:              clientSessionIDForRequest(ctx, in.env),
		EnabledProviders:             in.eligibility.EnabledProviders,
		CustomBindings:               in.eligibility.CustomBindings,
		GatewayProviders:             in.gatewayProviders,
		SubscriptionProviders:        s.selectableSubscriptionProviders(ctx, in.header),
		Candidates:                   prefilteredCandidates(ctx, in.prefiltered.Excluded, in.prefiltered),
		PreferredModels:              preferredModelsForRequest(ctx),
		RoutingKnobs:                 routingKnobsForRequest(ctx),
		ClusterArmOverrides:          clusterArmOverridesForRequest(ctx),
		ProductEligibility:           entitlement.ModelBoundaryFromContext(ctx),
	}
}
