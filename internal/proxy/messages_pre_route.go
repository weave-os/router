package proxy

import (
	"context"
	"encoding/hex"
	"log/slog"
	"net/http"
	"time"
	"weave-os/router/internal/conversation"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
)

type messagesPreRouteInput struct {
	w                    http.ResponseWriter
	r                    *http.Request
	body                 []byte
	log                  *slog.Logger
	env                  *translate.RequestEnvelope
	facts                requestcontext.RequestFacts
	strategy             router.Strategy
	clientBudget         router.ClientBudget
	agentShadowMode      bool
	agentForceModel      string
	requestBodyChanged   bool
	sessionKey           [sessionpin.SessionKeyLen]byte
	forceModelSessionKey [sessionpin.SessionKeyLen]byte
	feats                translate.RoutingFeatures
	promptText           string
	embedInput           string
}

type messagesPreRoute struct {
	feats                  translate.RoutingFeatures
	promptText             string
	embedInput             string
	taskArm                taskArmTurn
	req                    router.Request
	routeStart             time.Time
	outputReserve          int
	overflowEstimate       int
	inboundSpiralSignals   conversation.SpiralSignals
	inboundSpiralReasons   []conversation.SpiralReason
	inboundSpiralComputed  bool
	inboundToolCallCount   int
	inboundLastUser        translate.LastUserMessageInfo
	inboundUserPrompt      bool
	inboundLatestToolCalls []byte
}

func assembleMessagesPreRoute(ctx context.Context, s *Service, in messagesPreRouteInput) (context.Context, messagesPreRoute, bool, error) {
	w, r, body, log, env, facts, strategy, clientBudget := in.w, in.r, in.body, in.log, in.env, in.facts, in.strategy, in.clientBudget
	agentShadowMode, agentForceModel, requestBodyChanged := in.agentShadowMode, in.agentForceModel, in.requestBodyChanged
	sessionKey, forceModelSessionKey := in.sessionKey, in.forceModelSessionKey
	feats, promptText, embedInput := in.feats, in.promptText, in.embedInput

	if coalesced := env.CoalesceDuplicateToolResults(); coalesced > 0 {
		log.Info("Coalesced duplicate tool results before dispatch", "coalesced", coalesced)
		requestBodyChanged = true
	}
	if sanitized := env.SanitizeOrphanedToolCalls(); sanitized > 0 {
		log.Info("Sanitized orphaned tool calls before dispatch", "sanitized", sanitized)
		requestBodyChanged = true
	}
	if requestBodyChanged {
		feats = env.RoutingFeatures()
		ctx = requestcontext.WithRequestImageCount(ctx, feats.ImageCount)
		promptText, embedInput = turnPromptText(feats)
	}

	forceModel := agentForceModel
	forceCluster, taskArm := "", taskArmTurn{}
	if !agentShadowMode {
		var headerForceModel string
		var forceErr error
		ctx, headerForceModel, forceErr = s.applyForceModelHeader(ctx, r, facts.InstallationID, forceModelSessionKey)
		if forceErr != nil {
			return ctx, messagesPreRoute{}, false, forceErr
		}
		if headerForceModel != "" {
			forceModel = headerForceModel
		}
		forceCluster, taskArm, forceErr = forceClusterForTurn(s.strategyPorts, ctx, turnRequest{Strategy: strategy, InstallationID: facts.InstallationID}, r, env, feats, forceModel)
		if forceErr != nil {
			return ctx, messagesPreRoute{}, false, forceErr
		}
	}

	var inboundSpiralSignals conversation.SpiralSignals
	var inboundSpiralReasons []conversation.SpiralReason
	inboundSpiralComputed := s.ResolveTurnSignalCaptureEnabled(ctx)
	if inboundSpiralComputed {
		inboundSpiralSignals = conversation.ComputeSpiralSignals(env, feats.MessageCount)
		inboundSpiralReasons = conversation.SpiralReasons(inboundSpiralSignals)
	}

	if s.effectiveCaptureMode(ctx) != CaptureOff {
		logInboundRequestDiagnostics(log, env)
	}

	eligibility, eligibilityErr := requestEligibility(ctx, s, providers.ProviderAnthropic, r.Header)
	if eligibilityErr != nil {
		return ctx, messagesPreRoute{}, false, eligibilityErr
	}
	enabledProviders := eligibility.EnabledProviders

	if !conversation.PreparingHandoff(ctx) && s.serveNativeWebSearch(ctx, body, feats.Model, env.Stream(), feats.Tokens, enabledProviders, r.Header, w) {
		return ctx, messagesPreRoute{}, true, nil
	}

	if !conversation.PreparingHandoff(ctx) {
		if handled, searchErr := s.serveCodexWebSearch(ctx, body, feats.Model, env.Stream(), enabledProviders, r, w); handled {
			return ctx, messagesPreRoute{}, true, searchErr
		}
	}

	outputReserve, prefiltered := prefilterTurn(ctx, s, turnPrefilterInput{
		env: env, feats: feats, eligibility: eligibility, header: r.Header, excludeAdvisorOutranking: true, log: log,
	})
	inboundToolCallCount := len(env.AssistantToolCallSignatures())
	inboundLastUser := env.LastUserMessage()
	inboundUserPrompt := routingEndsWithUserPrompt(ctx, env)
	inboundLatestToolCalls := latestToolCallCountsJSON(env)
	overflowEstimate := prefiltered.OverflowEstimate

	routeStart := s.clock.Now()
	routingPromptText, routingMessages := routingInputForRequest(ctx, env, promptText)
	req := preRouteRequest(ctx, s, preRouteRequestInput{
		env: env, feats: feats, forceModel: forceModel, forceCluster: forceCluster,
		endpoint: router.EndpointAnthropicMessages, reasoningConfigHash: env.ReasoningConfigurationSHA256(), toolConfigHash: env.ToolConfigurationSHA256(),
		promptText: routingPromptText, messages: routingMessages, feedbackKey: hex.EncodeToString(sessionKey[:]),
		eligibility: eligibility, gatewayProviders: eligibility.GatewayProviders, header: r.Header, prefiltered: prefiltered,
	})
	req.ClientBudget, req.OrganizationID = clientBudget, facts.ExternalID
	if facts.InstallationID != uuid.Nil {
		req.InstallationID = facts.InstallationID.String()
	}
	return ctx, messagesPreRoute{
		feats: feats, promptText: promptText, embedInput: embedInput,
		taskArm: taskArm, req: req, routeStart: routeStart, outputReserve: outputReserve, overflowEstimate: overflowEstimate,
		inboundSpiralSignals: inboundSpiralSignals, inboundSpiralReasons: inboundSpiralReasons, inboundSpiralComputed: inboundSpiralComputed,
		inboundToolCallCount: inboundToolCallCount, inboundLastUser: inboundLastUser, inboundUserPrompt: inboundUserPrompt, inboundLatestToolCalls: inboundLatestToolCalls,
	}, false, nil
}
