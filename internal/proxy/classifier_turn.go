package proxy

import (
	"context"
	"fmt"

	"github.com/tidwall/gjson"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"
	"weave-os/router/internal/websearch"
)

type classifierInputContextKey struct{}

// Capture before command stripping, history rewriting or Responses projection.
func (s *Service) withClassifierInput(ctx context.Context, body []byte, endpoint router.TranslationEndpoint) (context.Context, error) {
	if router.StrategyFromContext(ctx) != router.StrategyLLMClassifier {
		return ctx, nil
	}
	_, captured := ctx.Value(classifierInputContextKey{}).(router.ClassifierContext)
	if _, shadow := AgentShadowEvalFromContext(ctx); shadow {
		return ctx, router.ErrClassifierThreadInvalid
	}
	if _, pinned := router.HonouredPolicyPin(ctx); pinned {
		return ctx, router.ErrClassifierThreadInvalid
	}
	if preparingHandoff(ctx) || gjson.GetBytes(body, piSessionField).Exists() || gjson.GetBytes(body, piHandoffField).Exists() {
		return ctx, router.ErrClassifierThreadInvalid
	}
	var observation translate.EscalationObservation
	var env *translate.RequestEnvelope
	var err error
	switch endpoint {
	case router.EndpointAnthropicMessages:
		env, err = translate.ParseAnthropic(body)
	case router.EndpointOpenAIChat:
		env, err = translate.ParseOpenAI(body)
	case router.EndpointGeminiGenerate:
		env, err = translate.ParseGemini(body)
	case router.EndpointOpenAIResponses:
		observation, err = translate.ParseResponsesEscalationObservation(body)
	default:
		return ctx, router.ErrClassifierThreadInvalid
	}
	if err == nil && env != nil {
		// Router commands do not represent model invocations and cannot advance
		// a classifier's causal chain through a synthetic assistant response.
		_, beta := env.ExtractBetaCommand()
		_, forced := env.ExtractForceModelCommand()
		_, feedback := env.ExtractRouterFeedbackCommand()
		if beta || forced || feedback || env.ExtractRouterSessionCommand() || env.ExtractRouterModelsCommand() {
			return ctx, router.ErrClassifierHistoryUnavailable
		}
		if !captured {
			observation, err = env.EscalationObservation()
		}
	}
	if err != nil {
		return ctx, fmt.Errorf("classifier input: %w: %w", err, router.ErrClassifierHistoryUnavailable)
	}
	if captured {
		return ctx, nil
	}
	input, err := classifierContextForCall(observation)
	if err != nil {
		observability.FromContext(ctx).Warn("Classifier input rejected", "err", err, "endpoint", endpoint)
		return ctx, err
	}
	if endpoint == router.EndpointAnthropicMessages && websearch.IsClaudeCodeWebSearchHelper(body) {
		ctx, err = s.withClassifierSearchChild(ctx, input)
		if err != nil {
			return ctx, err
		}
	}
	return context.WithValue(ctx, classifierInputContextKey{}, input), nil
}

// This path skips legacy sticky/utility/planner bypasses: every admitted action
// must classify its current API-call prefix before provider dispatch.
func (s *Service) runClassifierTurn(ctx context.Context, request router.Request, turn turnLoopResult, sessionKey [sessionpin.SessionKeyLen]byte) (turnLoopResult, error) {
	switch turn.TurnType {
	case turntype.TitleGen, turntype.Probe, turntype.Compaction, turntype.Classifier:
		observability.FromContext(ctx).Warn("Classifier thread rejects utility request", "turn_type", turn.TurnType)
		return turn, router.ErrClassifierHistoryUnavailable
	}
	input, ok := ctx.Value(classifierInputContextKey{}).(router.ClassifierContext)
	if !ok || request.ForceModel != "" || request.ForceCluster != "" {
		return turn, router.ErrClassifierThreadInvalid
	}
	prediction, err := s.classifyThread(ctx, input)
	if err != nil {
		return turn, err
	}
	request.ClassifierPrediction = &prediction
	turn.SessionKey, turn.AuthoritativePerTurn = sessionKey, true
	request.PolicyTurnContext = buildPolicyTurnContext(request, turn, sessionpin.Pin{}, sessionpin.Pin{})
	decision, err := s.routeFor(ctx, request)
	if err != nil {
		return turn, err
	}
	turn.Decision, turn.Fresh, turn.PinTier = decision, decision, string(router.StrategyLLMClassifier)
	if s.pinStore != nil {
		s.writeNewPin(ctx, turn.InstallationID, sessionKey, turn.PinRole, decision)
	}
	return turn, nil
}
