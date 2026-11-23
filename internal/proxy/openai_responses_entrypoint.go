package proxy

import (
	"context"
	"fmt"
	"net/http"
	"weave-os/router/internal/conversation"
	"weave-os/router/internal/ingress"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"

	"github.com/tidwall/gjson"
)

func (s *Service) ProxyOpenAIResponses(ctx context.Context, body []byte, w http.ResponseWriter, r *http.Request) error {
	w, finishBlindExperimentWriter := withBlindExperimentWriter(ctx, w, body)
	defer finishBlindExperimentWriter()
	ctx = withAttemptTrace(ctx, body, r.Header)
	ctx = context.WithValue(ctx, responsesSurfaceContextKey{}, true)
	ctx, _, inputErr := withTurnClassifierInput(ctx, s, body, router.EndpointOpenAIResponses)
	if inputErr != nil {
		return inputErr
	}
	ctx = s.withUsageObserver(ctx, r.Header)
	clientApp := requestcontext.ClientIdentityFrom(ctx).ClientApp
	portableCodex := clientApp == requestcontext.ClientAppCodex
	terminalResponses := requestcontext.SupportsResponsesTerminalSurfaces(clientApp)
	if translate.FeedbackFooterInResponsesHistory(body) {
		ctx = context.WithValue(ctx, responsesFooterEchoedContextKey{}, true)
	}
	ctx = withSubscriptionOnlyWarningEcho(ctx, body)
	nativeBody, conversionBody, err := s.ingress.Responses.DispatchBodies(body, terminalResponses)
	if err != nil {
		return err
	}
	if terminalResponses {
		ctx = context.WithValue(ctx, responsesLastAnnouncedModelContextKey{}, translate.LastAnnouncedResponsesModel(body))
	} else {
		ctx = context.WithValue(ctx, responsesReplayedModelContextKey{}, translate.LastReplayedResponsesModel(body))
	}
	conversion, err := s.ingress.Responses.ConvertCodexResponses(ctx, conversionBody, portableCodex, codexLossyRescueNeeded(ctx, s, r.Header))
	if err != nil {
		return fmt.Errorf("translate responses request: %w", err)
	}
	chatBody, model := conversion.Body, conversion.Model
	if len(conversion.RoutingBody) > 0 {
		ctx, err = withResponsesRoutingEnvelope(ctx, conversion.RoutingBody)
		if err != nil {
			return err
		}
	}
	if conversion.Requirements.NativeOnly {
		observability.FromContext(ctx).Info("Responses request restricted to its native wire family",
			"native_only_reasons", ingress.NativeOnlyReasons(conversion.Report, conversionBody))
	}
	if portableCodex && codexModelSwitched(r.Header, conversion) {
		ctx = withCodexSelectedModel(ctx, model, nativeBody)
	}
	ctx = conversation.WithCodexSkillTurn(ctx, conversion.CodexSkill)
	codexNativeRequest := codexResponsesRequest(ctx, r.Header)
	if conversion.Requirements.NativeOnly || codexNativeRequest {
		ctx = context.WithValue(ctx, codexResponsesBodyContextKey{}, nativeBody)
	}
	ctx = context.WithValue(ctx, nativeResponsesBodyContextKey{}, nativeBody)
	if conversion.Requirements.NativeOnly || (portableCodex && codexNativeRequest) {
		originalEnvelope, parseErr := translate.ParseOpenAI(nativeBody)
		if parseErr != nil {
			return fmt.Errorf("parse native Responses request: %w", parseErr)
		}
		ctx = context.WithValue(ctx, nativeResponsesReasoningHashContextKey{}, originalEnvelope.ReasoningConfigurationSHA256())
		ctx = context.WithValue(ctx, nativeResponsesToolHashContextKey{}, originalEnvelope.ToolConfigurationSHA256())
	}
	ctx = context.WithValue(ctx, responsesRequirementsContextKey{}, conversion.Requirements)
	if portableCodex && conversion.TitleGeneration {
		ctx = context.WithValue(ctx, codexTitleGenerationContextKey{}, true)
	}
	ctx = context.WithValue(ctx, responsesTransformsContextKey{}, conversion.Report)
	wrapper := translate.NewResponsesWriter(w, model)
	if terminalResponses {
		wrapper.EnableTerminalBadgeProvenance()
	}
	wrapper.SetToolMappings(conversion.ToolMappings)
	ctx, deferredLog := withDeferredCallLog(ctx)
	deferredLog.requestBody = body
	proxyErr := s.ProxyOpenAIChatCompletion(ctx, chatBody, wrapper, r)
	if proxyErr != nil {
		if msg := TerminalErrorMessage(proxyErr); msg != upstreamCallFailedMessage {
			wrapper.SetUpstreamFailureMessage(msg)
		}
		finalize := func() error { return wrapper.FinalizeError(proxyErr) }
		if isContextOverflow(proxyErr) {
			finalize = func() error {
				return wrapper.FailContextOverflow(gjson.GetBytes(chatBody, "stream").Bool(), ingress.OpenAIContextOverflowCode, contextOverflowMessage)
			}
		}
		if finErr := finalize(); finErr != nil {
			observability.FromContext(ctx).Error("Failed to finalize Responses error stream", "err", finErr)
		}
		if deferredLog.escalation != nil {
			deferredLog.escalation(proxyErr)
		}
		deferredLog.run()
		return proxyErr
	}
	finErr := wrapper.Finalize()
	if deferredLog.escalation != nil {
		deferredLog.escalation(finErr)
	}
	deferredLog.run()
	return finErr
}
