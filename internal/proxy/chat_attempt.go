package proxy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"weave-os/router/internal/routingpolicy"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/ingress"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/telemetry"
	"weave-os/router/internal/translate"
	"weave-os/router/internal/translate/toolcheck"
	providerusage "weave-os/router/internal/usage"

	"github.com/tidwall/sjson"
)

type chatAttemptDeps struct {
	request              *http.Request
	svc                  *Service
	isResponses          bool
	responseSwitch       *responseProtocolSwitch
	facts                requestcontext.RequestFacts
	responsesNotices     *requestNoticeStream
	responsesPreludeBuf  *preludeBuffer
	env                  *translate.RequestEnvelope
	responseWriter       http.ResponseWriter
	responsesBody        []byte
	log                  *slog.Logger
	fastServed           *bool
	routeRes             *routingpolicy.TurnRoute
	makeMarkerSink       func(notice translate.RouterNotice) (http.ResponseWriter, error)
	cyberRetryArmed      *bool
	preOutputRescueArmed *bool
	extractor            **telemetry.UsageExtractor
	toolValidator        *toolcheck.Validator
	responsesWriter      *translate.ResponsesWriter
	preludeBuf           *preludeBuffer
	sink                 http.ResponseWriter
	respSummary          *translate.ResponseSummary
	nativeRespSummary    *bool
	cyberRefusalSeen     *bool
	responsesEndpointKey string
	crossFormat          *bool
}

func buildChatAttempt(ctx context.Context, deps chatAttemptDeps, target router.Decision, targetOpts translate.EmitOptions, targetMarker string, fundingCtx context.Context) (dispatch.UpstreamAttempt, error) {
	targetNotice := routerNoticeForDecision(withOwnCodexRejectionNudge(ctx, deps.request.Header, deps.svc.credentials.UsageObserver), deps.request.Header, target, targetMarker)
	targetNotice = addRoutingFundingSource(fundingCtx, seatAdmissionOf(deps.svc), target.Provider, target.Model, targetNotice)
	if deps.isResponses {
		if targetMarker != "" && (!deps.responseSwitch.passthrough() || requestcontext.SupportsResponsesTerminalSurfaces(deps.facts.Client.ClientApp)) {
			_, noticeErr := deps.responsesNotices.prepare(ctx, deps.responsesPreludeBuf, deps.responsesPreludeBuf, translate.FormatOpenAIResponses, deps.env.Stream(), targetNotice)
			if noticeErr != nil {
				return nil, fmt.Errorf("emit Responses router notice: %w", noticeErr)
			}
		}
	}
	switch providers.FamilyFor(target.Provider) {
	case providers.FamilyOpenAICompat:
		dispatchOpenAI := func(actx context.Context, d router.Decision, p dispatch.Upstream, surface openAISurface, stripPromptCacheKey bool) error {
			resetResponsesAttempt(deps.responseWriter)
			var prep providers.PreparedRequest
			switch surface {
			case surfaceResponsesNative:
				outBody, setErr := sjson.SetBytes(deps.responsesBody, "model", d.Model)
				if setErr != nil {
					deps.log.Error("Failed to set routed model on Codex Responses body", "err", setErr, "decision_model", d.Model)
					return fmt.Errorf("set codex model: %w", setErr)
				}
				outBody, setErr = translate.ClampResponsesInputCallIDs(outBody)
				if setErr != nil {
					return fmt.Errorf("clamp codex call_id: %w", setErr)
				}
				nativeOpts := targetOpts
				nativeOpts.TargetProvider = d.Provider
				nativeOpts.FastMode = fastModeForAttempt(actx, d.Model, d.Provider)
				*deps.fastServed = nativeOpts.FastMode
				outBody, setErr = codexNativeFastMode(outBody, nativeOpts, deps.routeRes.TurnType)
				if setErr != nil {
					return fmt.Errorf("set codex service_tier: %w", setErr)
				}
				outBody, setErr = codexNativeEffort(outBody, nativeOpts, deps.routeRes.TurnType)
				if setErr != nil {
					return fmt.Errorf("set codex reasoning effort: %w", setErr)
				}
				prep = providers.PreparedRequest{
					Body:     outBody,
					Endpoint: providers.EndpointResponses,
					Headers:  make(http.Header),
					Stats: providers.RequestMutationStats{
						Transformations: responseTransformationsFromContext(actx),
					},
				}
			default:
				attemptOpts := targetOpts
				attemptOpts.TargetProvider = d.Provider
				attemptOpts.StripPromptCacheKey = stripPromptCacheKey
				attemptOpts.FastMode = fastModeForAttempt(actx, d.Model, d.Provider)
				attemptOpts.ReasoningReplayScope = deps.svc.reasoningReplayScope(actx, d)
				*deps.fastServed = attemptOpts.FastMode
				var emitErr error
				if surface == surfaceResponsesTranslated {
					prep, emitErr = deps.env.PrepareOpenAIResponses(deps.request.Header, attemptOpts)
				} else {
					prep, emitErr = prepareOpenAIForTurn(deps.env, deps.request.Header, attemptOpts, deps.routeRes.TurnType)
				}
				if emitErr != nil {
					deps.log.Error("Failed to emit OpenAI body", "err", emitErr,
						"decision_provider", d.Provider, "endpoint", prep.Endpoint)
					return fmt.Errorf("emit body: %w", emitErr)
				}
			}
			noticeSink, noticeErr := deps.makeMarkerSink(reconnectNoticeForAttempt(actx, deps.request.Header, targetNotice))
			if noticeErr != nil {
				return fmt.Errorf("emit OpenAI router notice: %w", noticeErr)
			}
			refusalGate := newCyberRefusalGate(noticeSink,
				*deps.cyberRetryArmed && d.Provider == providers.ProviderOpenAI)
			if surface == surfaceResponsesNative && deps.env.Stream() && *deps.preOutputRescueArmed {
				refusalGate.holdPreOutputFailure()
			}
			var attemptSink http.ResponseWriter = refusalGate
			var seedToolCalls *translate.SeedToolCallChatWriter
			if surface == surfaceChat {
				if names := deps.env.SeedToolCallNames(prep.ResponseToolNames); len(names) > 0 {
					seedToolCalls = translate.NewSeedToolCallChatWriter(attemptSink, names)
					attemptSink = seedToolCalls
				}
			}
			proxyWriter := attemptSink
			var translator *translate.ResponsesToOpenAIChatWriter
			var toolNames *translate.ToolNameWriter
			var nativeTerminal *ingress.ResponsesTerminalObserver
			switch {
			case surface == surfaceResponsesTranslated:
				var usage translate.UsageSink
				if deps.svc.usageRequired() {
					*deps.extractor = telemetry.NewUsageExtractor(d.Provider)
					usage = *deps.extractor
				}
				translator = translate.NewResponsesToOpenAIChatWriter(attemptSink, d.Model, usage).
					WithLogger(deps.log).
					WithToolValidator(deps.toolValidator)
				if providers.OutputExcludesReasoning(d.Provider) {
					translator.SetUsageOutputBasis(providerusage.OutputExcludesReasoning)
				}
				if deps.isResponses {
					deps.responsesWriter.SetUsageSource(translator.UsageFacts)
				}
				if err := translator.Prelude(deps.env.Stream()); err != nil {
					deps.log.Error("chat/completions prelude failed (Responses upstream)", "err", err)
				}
				toolNames = translate.NewOpenAIToolNameWriter(translator, prep.ResponseToolNames)
				proxyWriter = toolNames
			case surface == surfaceResponsesNative:
				nativeTerminal = ingress.NewResponsesTerminalObserver(attemptSink)
				proxyWriter = nativeTerminal
				if deps.svc.usageRequired() {
					*deps.extractor, proxyWriter = deps.svc.turnTelemetry.newUsageExtractor(nativeTerminal, d.Provider)
				}
			case deps.svc.usageRequired() || deps.isResponses:
				*deps.extractor, proxyWriter = deps.svc.turnTelemetry.newUsageExtractor(attemptSink, d.Provider)
			}
			if surface == surfaceChat && deps.isResponses {
				deps.responsesWriter.SetUsageSource((*deps.extractor).UsageFacts)
			}
			var chatTerminal *streamTerminalObserver
			if surface == surfaceChat && !deps.isResponses && deps.env.Stream() {
				chatTerminal = newStreamTerminalObserver(proxyWriter, nativeStreamOpenAIChat)
				proxyWriter = chatTerminal
			}
			if deps.preludeBuf != nil {
				deps.preludeBuf.Seal()
			}
			err := p.Proxy(actx, d, prep, proxyWriter, deps.request)
			if nativeTerminal != nil && nativeTerminal.Observed() && errors.Is(err, context.Canceled) {
				err = nil
			}
			if seedToolCalls != nil {
				err = finalizeAfterProxy(err, seedToolCalls.Finalize)
				if n := seedToolCalls.ConvertedToolCalls(); n > 0 {
					deps.log.Warn("Recovered tool calls from Seed text markup",
						"model", d.Model,
						"provider", d.Provider,
						"tool_calls", n)
				}
			}
			if err == nil && chatTerminal != nil {
				err = chatTerminal.streamErr()
			}
			if err != nil && surface != surfaceResponsesNative && deps.env.Stream() && deps.preludeBuf.Committed() {
				_ = emitOpenAISSEErrorEvent(deps.sink, attributeStreamError(actx, d.Provider, err))
			}
			if translator != nil {
				err = finalizeAfterProxy(finalizeAfterProxy(err, toolNames.Finalize), translator.Finalize)
				if err == nil {
					err = translator.UpstreamError()
				}
				*deps.respSummary = translator.Summary()
				*deps.nativeRespSummary = false
			} else if nativeTerminal != nil {
				nativeTerminal.Finalize()
				if rw, ok := deps.responseWriter.(*translate.ResponsesWriter); ok {
					err = finalizeAfterProxy(err, rw.Finalize)
				}
				if err == nil && deps.env.Stream() {
					err = nativeTerminal.Err()
				}
				if isStreamTerminalMissing(err) {
					refusalGate.Abandon()
				}
				if nativeTerminal.Observed() && nativeTerminal.Signals().OutputLimitReached {
					(*deps.extractor).RecordOutputLimitReached()
				}
				*deps.respSummary = translate.ResponseSummary{UpstreamFinishReason: nativeTerminal.Signals().FinishReason}
				*deps.nativeRespSummary = false
				if nativeTerminal.Observed() && deps.svc.ResolveNativeOpenAIResponseSignals(actx) {
					deps.respSummary.StopReason = nativeTerminal.Signals().FinishReason
					deps.respSummary.ToolUseBlocks = nativeTerminal.Signals().ToolCalls
					*deps.nativeRespSummary = true
				}
			}
			if releaseErr := refusalGate.finalizeAttempt(err); releaseErr != nil && (err == nil || providers.IsUpstreamCyberPolicyRefusal(releaseErr)) {
				err = releaseErr
			}
			if err != nil && refusalGate.holdFailure && refusalGate.releaseEvent != "" {
				deps.log.Warn("Held Responses stream released before upstream failure",
					"model", d.Model,
					"provider", d.Provider,
					"release_event", refusalGate.releaseEvent,
					"release_item_type", refusalGate.releaseItemType,
					"err", err)
			}
			if refusalGate.refused {
				*deps.cyberRefusalSeen = true
			}
			if refusalGate.withheld {
				err = providers.CyberPolicyRefusalError()
			}

			return err
		}
		return func(actx context.Context, d router.Decision, p dispatch.Upstream) error {
			surface := deps.responseSwitch.surface(d.Provider, servedOnCodexSubscription(actx))
			gatewayKey := gatewayResponsesKey(actx, d.Provider)
			stripPromptCacheKey := deps.svc.engine.GatewayCaps().RejectsPromptCacheKey(gatewayKey)
			err := dispatchOpenAI(actx, d, p, surface, stripPromptCacheKey)
			if err != nil && !stripPromptCacheKey && gatewayKey != "" && !committed(deps.preludeBuf) &&
				providers.IsUpstreamPromptCacheKeyRejection(err) {
				deps.svc.engine.GatewayCaps().RememberRejectsPromptCacheKey(gatewayKey)
				stripPromptCacheKey = true
				deps.log.Warn("Gateway rejected prompt_cache_key; retrying without the affinity hint",
					"model", d.Model,
					"decision_provider", d.Provider)
				if deps.preludeBuf != nil {
					deps.preludeBuf.Discard()
				}
				err = dispatchOpenAI(actx, d, p, surface, true)
			}
			if err == nil || surface == surfaceChat ||
				committed(deps.preludeBuf) || !providers.IsUpstreamResponsesUnsupported(err) ||
				codexSubscriptionModelRejected(err) {
				return err
			}
			if deps.responseSwitch.fallbackToChat(surface) != nil {
				return err
			}
			deps.svc.engine.GatewayCaps().RememberLacksResponses(deps.responsesEndpointKey)
			deps.log.Warn("OpenAI endpoint rejected the Responses API; retrying on chat/completions",
				"model", d.Model,
				"decision_provider", d.Provider)
			if deps.preludeBuf != nil {
				deps.preludeBuf.Discard()
			}
			return dispatchOpenAI(actx, d, p, surfaceChat, stripPromptCacheKey)
		}, nil
	case providers.FamilyGemini:
		*deps.crossFormat = true
		prep, emitErr := deps.env.PrepareGemini(deps.request.Header, targetOpts)
		if emitErr != nil {
			deps.log.Error("Failed to translate OpenAI request to Gemini format", "err", emitErr)
			return nil, fmt.Errorf("translate openai request to gemini: %w", emitErr)
		}
		dispatchGemini := func(actx context.Context, d router.Decision, p dispatch.Upstream, pr providers.PreparedRequest) (func(error) error, error) {
			resetResponsesAttempt(deps.responseWriter)
			var usage translate.UsageSink
			if deps.svc.usageRequired() {
				*deps.extractor = telemetry.NewUsageExtractor(d.Provider)
				usage = *deps.extractor
			}
			attemptSink, noticeErr := deps.makeMarkerSink(reconnectNoticeForAttempt(actx, deps.request.Header, targetNotice))
			if noticeErr != nil {
				return func(err error) error { return err }, noticeErr
			}
			translator := translate.NewGeminiToOpenAISSETranslator(attemptSink, d.Model, usage)
			if deps.isResponses {
				deps.responsesWriter.SetUsageSource(translator.UsageFacts)
			}
			if deps.preludeBuf != nil {
				deps.preludeBuf.Seal()
			}
			rawErr := p.Proxy(actx, d, pr, translator, deps.request)
			finalize := func(err error) error {
				if err != nil && deps.env.Stream() && deps.preludeBuf.Committed() {
					_ = emitOpenAISSEErrorEvent(deps.sink, attributeStreamError(actx, d.Provider, err))
				}
				return finalizeAfterProxy(err, translator.Finalize)
			}
			return finalize, rawErr
		}
		return func(actx context.Context, d router.Decision, p dispatch.Upstream) error {
			retry := geminiToolModeAttempt{log: deps.log, buffer: deps.preludeBuf, env: deps.env, headers: deps.request.Header, opts: targetOpts}
			return retry.dispatch(prep, func(prepared providers.PreparedRequest) (func(error) error, error) {
				return dispatchGemini(actx, d, p, prepared)
			})
		}, nil
	case providers.FamilyAnthropic:
		*deps.crossFormat = true
		prep, emitErr := deps.env.PrepareAnthropic(deps.request.Header, targetOpts)
		if emitErr != nil {
			deps.log.Error("Failed to translate OpenAI request to Anthropic format", "err", emitErr)
			return nil, fmt.Errorf("translate openai request: %w", emitErr)
		}
		dispatchAnthropic := func(actx context.Context, d router.Decision, p dispatch.Upstream, attemptPrep providers.PreparedRequest, fast bool) (func(error) error, error) {
			resetResponsesAttempt(deps.responseWriter)
			*deps.fastServed = fast
			var usage translate.UsageSink
			if deps.svc.usageRequired() {
				*deps.extractor = telemetry.NewUsageExtractor(providers.ProviderAnthropic)
				(*deps.extractor).SetRequestCacheTTL1h(func() bool { return translate.AnthropicRequestCacheTTL1h(attemptPrep.Body) })
				usage = *deps.extractor
			}
			attemptSink, noticeErr := deps.makeMarkerSink(reconnectNoticeForAttempt(actx, deps.request.Header, targetNotice))
			if noticeErr != nil {
				return func(err error) error { return err }, noticeErr
			}
			translator := translate.NewSSETranslator(attemptSink, d.Model, usage)
			if deps.isResponses {
				deps.responsesWriter.SetUsageSource(translator.UsageFacts)
			}
			if deps.preludeBuf != nil {
				deps.preludeBuf.Seal()
			}
			toolNames := translate.NewAnthropicToolNameWriter(translator, attemptPrep.ResponseToolNames)
			rawErr := p.Proxy(actx, d, attemptPrep, toolNames, deps.request)
			finalize := func(err error) error {
				if err != nil && deps.env.Stream() && deps.preludeBuf.Committed() {
					_ = emitOpenAISSEErrorEvent(deps.sink, attributeStreamError(actx, d.Provider, err))
				}
				err = finalizeAfterProxy(finalizeAfterProxy(err, toolNames.Finalize), translator.Finalize)
				*deps.respSummary = translator.Summary()
				*deps.nativeRespSummary = false
				return err
			}
			return finalize, rawErr
		}
		return func(actx context.Context, d router.Decision, p dispatch.Upstream) error {
			attemptOpts := targetOpts
			attemptOpts.TargetProvider = d.Provider
			attemptOpts.FastMode = fastModeForAttempt(actx, d.Model, d.Provider)
			attemptPrep := prep
			if attemptOpts.FastMode != targetOpts.FastMode {
				var emitErr error
				attemptPrep, emitErr = deps.env.PrepareAnthropic(deps.request.Header, attemptOpts)
				if emitErr != nil {
					deps.log.Error("Failed to re-translate OpenAI request to Anthropic format for fast-tier change", "err", emitErr)
					return fmt.Errorf("translate openai request: %w", emitErr)
				}
			}
			finalize, rawErr := dispatchAnthropic(actx, d, p, attemptPrep, attemptOpts.FastMode)
			rawErr, _ = retryAnthropicStandardSpeed(deps.log, deps.preludeBuf, d, attemptOpts, rawErr, func(standardOpts translate.EmitOptions) (func() error, error) {
				standardPrep, emitErr := deps.env.PrepareAnthropic(deps.request.Header, standardOpts)
				if emitErr != nil {
					return nil, emitErr
				}
				return func() error {
					var standardErr error
					finalize, standardErr = dispatchAnthropic(actx, d, p, standardPrep, false)
					return standardErr
				}, nil
			})
			return finalize(rawErr)
		}, nil
	default:
		return nil, fmt.Errorf("%w: %s (no translation path defined)", ErrProviderNotConfigured, target.Provider)
	}
}
