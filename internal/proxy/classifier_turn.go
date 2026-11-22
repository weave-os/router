package proxy

import (
	"context"
	"errors"
	"fmt"

	"github.com/tidwall/gjson"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/routingpolicy"
	"weave-os/router/internal/translate"
	"weave-os/router/internal/websearch"
)

type classifierInputContextKey struct{}

func (s *Service) withClassifierInput(ctx context.Context, strategy router.Strategy, body []byte, endpoint router.TranslationEndpoint) (context.Context, error) {
	if auth.RoutingPassthroughFrom(ctx) {
		return ctx, nil
	}
	if !router.ClassifiesThreads(strategy) {
		return ctx, nil
	}
	_, captured := ctx.Value(classifierInputContextKey{}).(router.ClassifierContext)
	if _, shadow := requestcontext.AgentShadowEvalFromContext(ctx); shadow {
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
		_, beta := env.ExtractBetaCommand()
		_, forced := env.ExtractForceModelCommand()
		_, feedback := env.ExtractRouterFeedbackCommand()
		if beta || forced || feedback || env.ExtractRouterSessionCommand() || env.ExtractRouterModelsCommand() || env.ExtractListModelsCommand() {
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
	observation.CodexToolResults = endpoint == router.EndpointOpenAIResponses && ClientIdentityFrom(ctx).ClientApp == requestcontext.ClientAppCodex
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

func (s *Service) runClassifierTurn(ctx context.Context, strategy router.Strategy, request router.Request, turn turnLoopResult, sessionKey [sessionpin.SessionKeyLen]byte) (turnLoopResult, error) {
	input, captured := ctx.Value(classifierInputContextKey{}).(router.ClassifierContext)
	if err := routingpolicy.AdmitClassifierTurn(turn.TurnType, captured, request); err != nil {
		if errors.Is(err, router.ErrClassifierHistoryUnavailable) {
			observability.FromContext(ctx).Warn("Classifier thread rejects utility request", "turn_type", turn.TurnType)
		}
		return turn, err
	}
	prediction, err := s.classifyThread(ctx, input)
	if err != nil {
		return turn, err
	}
	request.ClassifierPrediction = &prediction
	unpinned := isUnpinnedScoredTurn(turn.TurnType)
	if !unpinned {
		turn.SessionKey = sessionKey
	}
	turn.AuthoritativePerTurn = true
	request.Session.PolicyTurnContext = buildPolicyTurnContext(request, turn, sessionpin.Pin{}, sessionpin.Pin{}, s.clock.Now())
	decision, err := s.routeFor(ctx, strategy, request)
	if err != nil {
		return turn, err
	}
	turn.Decision, turn.Fresh, turn.PinTier = decision, decision, string(strategy)
	if s.pinStore != nil && !unpinned {
		routingpolicy.NewSessionPins(s.pinStore, s.clock.Now, strategy).WriteNew(ctx, turn.InstallationID, sessionKey, turn.PinRole, decision)
	}
	return turn, nil
}
