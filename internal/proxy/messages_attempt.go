package proxy

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"weave-os/router/internal/routingpolicy"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/subscriptions"
	"weave-os/router/internal/telemetry"
	"weave-os/router/internal/translate"
	"weave-os/router/internal/translate/toolcheck"
	providerusage "weave-os/router/internal/usage"
)

type messagesAttemptDeps struct {
	request                 *http.Request
	svc                     *Service
	env                     *translate.RequestEnvelope
	routeRes                *routingpolicy.TurnRoute
	log                     *slog.Logger
	crossFormat             *bool
	feats                   translate.RoutingFeatures
	anthropicTierAttemptFor func(targetOpts translate.EmitOptions, prep providers.PreparedRequest, targetNotice translate.RouterNotice) *anthropicTierAttempt
	recordFastServed        func(fast bool)
	preludeBuf              *preludeBuffer
	sink                    http.ResponseWriter
	anthropicPrelude        *requestNoticeStream
	streamCut               *streamCutObserver
	setExtractor            func(e *telemetry.UsageExtractor)
	setStreamCost           func(d router.Decision, inputIncludesCache bool, requestCacheTTL1h func() bool)
	fastServed              *bool
	respSummary             *translate.ResponseSummary
	reqStats                *providers.RequestMutationStats
	streamCost              *streamCostWriter
	extractor               **telemetry.UsageExtractor
	toolValidator           *toolcheck.Validator
}

func buildMessagesAttempt(ctx context.Context, deps messagesAttemptDeps, target router.Decision, targetOpts translate.EmitOptions, targetMarker string, fundingCtx context.Context) (dispatch.UpstreamAttempt, error) {
	targetNotice := routerNoticeForRequest(withOwnClaudeRejectionNudge(ctx, deps.request.Header, deps.svc.credentials.UsageObserver), deps.request.Header, subscriptions.ProviderClaude, target.Model, targetMarker)
	targetNotice = addRoutingFundingSource(fundingCtx, seatAdmissionOf(deps.svc), target.Provider, target.Model, targetNotice)
	switch providers.FamilyFor(target.Provider) {
	case providers.FamilyAnthropic:
		prep, emitErr := prepareAnthropicForTurn(deps.env, deps.request.Header, targetOpts, deps.routeRes.TurnType)
		if emitErr != nil {
			deps.log.Error("Failed to emit Anthropic body", "err", emitErr)
			return nil, fmt.Errorf("emit body: %w", emitErr)
		}
		*deps.crossFormat = false
		logUpstreamBody(deps.log, deps.routeRes.SessionKey, target, deps.feats, prep.Body)
		tiered := deps.anthropicTierAttemptFor(targetOpts, prep, targetNotice)
		return func(actx context.Context, d router.Decision, p dispatch.Upstream) error {
			attemptOpts, err := tiered.dispatch(actx, d, p, deps.recordFastServed)
			if err == nil || committed(deps.preludeBuf) || !providers.IsUpstreamOutputConfigFormatRejection(err) {
				return err
			}
			unstructuredOpts := attemptOpts
			unstructuredOpts.StripOutputConfigFormat = true
			unstructuredPrep, emitErr := deps.env.PrepareAnthropic(deps.request.Header, unstructuredOpts)
			if emitErr != nil {
				deps.log.Error("Failed to re-emit Anthropic body without output_config.format", "err", emitErr)
				return err
			}
			deps.log.Warn("Retrying Anthropic request without output_config.format after upstream rejected it",
				"model", d.Model,
				"provider", d.Provider)
			if deps.preludeBuf != nil {
				deps.preludeBuf.Discard()
			}
			logUpstreamBody(deps.log, deps.routeRes.SessionKey, target, deps.feats, unstructuredPrep.Body)
			return deps.svc.anthropicNativeAttempt(deps.env, deps.request, unstructuredPrep, deps.sink, deps.preludeBuf, deps.anthropicPrelude, deps.streamCut, targetNotice, deps.setExtractor, deps.setStreamCost)(actx, d, p)
		}, nil
	case providers.FamilyOpenAICompat:
		*deps.crossFormat = true
		dispatchOpenAICompat := func(actx context.Context, d router.Decision, p dispatch.Upstream, useResponses, stripPromptCacheKey bool) (func(error) error, error) {
			attemptOpts := targetOpts
			attemptOpts.TargetProvider = d.Provider
			attemptOpts.StripPromptCacheKey = stripPromptCacheKey
			attemptOpts.FastMode = fastModeForAttempt(actx, d.Model, d.Provider)
			attemptOpts.ReasoningReplayScope = deps.svc.reasoningReplayScope(actx, d)
			*deps.fastServed = attemptOpts.FastMode
			deps.setStreamCost(d, true, nil)
			*deps.respSummary = translate.ResponseSummary{}
			var prep providers.PreparedRequest
			var emitErr error
			if useResponses {
				prep, emitErr = deps.env.PrepareOpenAIResponses(deps.request.Header, attemptOpts)
			} else {
				prep, emitErr = deps.env.PrepareOpenAI(deps.request.Header, attemptOpts)
			}
			if emitErr != nil {
				deps.log.Error("Failed to translate Anthropic request to OpenAI format", "err", emitErr, "decision_provider", d.Provider, "responses_api", useResponses)
				return func(err error) error { return err }, fmt.Errorf("translate anthropic request: %w", emitErr)
			}
			*deps.reqStats = prep.Stats
			logUpstreamBody(deps.log, deps.routeRes.SessionKey, d, deps.feats, prep.Body)
			var usage translate.UsageSink
			if deps.svc.usageRequired() || deps.streamCost != nil {
				*deps.extractor = telemetry.NewUsageExtractor(d.Provider)
				usage = *deps.extractor
			}
			var translator translate.ResponseTranslator
			attemptSink, noticeErr := deps.anthropicPrelude.prepare(actx, deps.sink, deps.preludeBuf, translate.FormatAnthropic, deps.env.Stream(), reconnectNoticeForAttempt(actx, deps.request.Header, targetNotice))
			if noticeErr != nil {
				return func(err error) error { return err }, noticeErr
			}
			if useResponses {
				responsesTranslator := translate.NewResponsesToAnthropicWriter(attemptSink, d.Model, usage).
					WithReasoningScope(attemptOpts.ReasoningReplayScope).
					WithEstimatedInputTokens(deps.feats.Tokens).
					WithRequestHadTools(deps.feats.HasTools).
					WithToolValidator(deps.toolValidator)
				if providers.OutputExcludesReasoning(d.Provider) {
					responsesTranslator.SetUsageOutputBasis(providerusage.OutputExcludesReasoning)
				}
				responsesTranslator.SetClientInputTokenScale(clientInputTokenScale(*deps.routeRes, d.Provider, clientRequestedModel(ctx, deps.feats.Model)))
				if err := responsesTranslator.Prelude(deps.env.Stream()); err != nil {
					return func(err error) error { return err }, fmt.Errorf("emit Anthropic routing marker: %w", err)
				}
				translator = responsesTranslator
			} else {
				chatTranslator := translate.NewAnthropicSSETranslator(attemptSink, d.Model, usage).
					WithLogger(deps.log).
					WithEstimatedInputTokens(deps.feats.Tokens).
					WithRequestHadTools(deps.feats.HasTools).
					WithThinkTagReasoning(catalog.ThinkTagReasoningFor(d.Model)).
					WithSeedToolCallMarkup(deps.env.SeedToolCallNames(prep.ResponseToolNames)).
					WithEscapeNormalize(flags.BoolOr(ctx, flags.KeyEscapeNormalize, deps.svc.escapeNormalize)).
					WithToolValidator(deps.toolValidator)
				if providers.OutputExcludesReasoning(d.Provider) {
					chatTranslator.SetUsageOutputBasis(providerusage.OutputExcludesReasoning)
				}
				chatTranslator.SetClientInputTokenScale(clientInputTokenScale(*deps.routeRes, d.Provider, clientRequestedModel(ctx, deps.feats.Model)))
				if err := chatTranslator.Prelude(deps.env.Stream()); err != nil {
					return func(err error) error { return err }, fmt.Errorf("emit Anthropic routing marker: %w", err)
				}
				translator = chatTranslator
			}
			if deps.preludeBuf != nil {
				deps.preludeBuf.Seal()
			}
			toolNames := translate.NewOpenAIToolNameWriter(translator, prep.ResponseToolNames)
			rawErr := p.Proxy(actx, d, prep, deps.streamCut.attach(toolNames), deps.request)
			finalize := func(err error) error {
				if err != nil && deps.env.Stream() && deps.preludeBuf.Committed() {
					deps.streamCut.noteCut(err)
					err = emitAnthropicSSEErrorEvent(deps.sink, attributeStreamError(actx, d.Provider, err))
				}
				finErr := finalizeAfterProxy(finalizeAfterProxy(err, toolNames.Finalize), translator.Finalize)
				*deps.respSummary = translator.Summary()
				return finErr
			}
			return finalize, rawErr
		}
		return func(actx context.Context, d router.Decision, p dispatch.Upstream) error {
			gatewayKey := gatewayResponsesKey(actx, d.Provider)
			useResponses := translate.UseOpenAIResponsesAPI(translate.ResponsesRoute{
				Provider:       d.Provider,
				Capabilities:   targetOpts.Capabilities,
				HasTools:       deps.feats.HasTools,
				ChatOnlyParams: deps.env.RequiresChatCompletionsParams(targetOpts.Capabilities),
				Broad:          deps.svc.ResolveOpenAIResponsesBroad(actx),
			}) && !deps.svc.engine.GatewayCaps().LacksResponses(gatewayKey)
			stripPromptCacheKey := deps.svc.engine.GatewayCaps().RejectsPromptCacheKey(gatewayKey)
			finalize, rawErr := dispatchOpenAICompat(actx, d, p, useResponses, stripPromptCacheKey)
			if rawErr != nil && useResponses && !committed(deps.preludeBuf) &&
				providers.IsUpstreamResponsesUnsupported(rawErr) {
				deps.svc.engine.GatewayCaps().RememberLacksResponses(gatewayKey)
				deps.log.Warn("Gateway rejected the Responses API; retrying on chat/completions",
					"model", d.Model,
					"decision_provider", d.Provider)
				if deps.preludeBuf != nil {
					deps.preludeBuf.Discard()
				}
				useResponses = false
				finalize, rawErr = dispatchOpenAICompat(actx, d, p, false, stripPromptCacheKey)
			}
			if rawErr != nil && !stripPromptCacheKey && gatewayKey != "" && !committed(deps.preludeBuf) &&
				providers.IsUpstreamPromptCacheKeyRejection(rawErr) {
				deps.svc.engine.GatewayCaps().RememberRejectsPromptCacheKey(gatewayKey)
				deps.log.Warn("Gateway rejected prompt_cache_key; retrying without the affinity hint",
					"model", d.Model,
					"decision_provider", d.Provider)
				if deps.preludeBuf != nil {
					deps.preludeBuf.Discard()
				}
				finalize, rawErr = dispatchOpenAICompat(actx, d, p, useResponses, true)
			}
			return finalize(rawErr)
		}, nil
	case providers.FamilyGemini:
		prep, emitErr := deps.env.PrepareGemini(deps.request.Header, targetOpts)
		*deps.reqStats = prep.Stats
		if emitErr != nil {
			deps.log.Error("Failed to translate Anthropic request to Gemini format", "err", emitErr)
			return nil, fmt.Errorf("translate anthropic request to gemini: %w", emitErr)
		}
		*deps.crossFormat = true
		logUpstreamBody(deps.log, deps.routeRes.SessionKey, target, deps.feats, prep.Body)
		dispatchGemini := func(actx context.Context, d router.Decision, p dispatch.Upstream, pr providers.PreparedRequest) (func(error) error, error) {
			*deps.fastServed = false
			deps.setStreamCost(d, true, nil)
			*deps.respSummary = translate.ResponseSummary{}
			var usage translate.UsageSink
			if deps.svc.usageRequired() || deps.streamCost != nil {
				*deps.extractor = telemetry.NewUsageExtractor(d.Provider)
				usage = *deps.extractor
			}
			attemptSink, noticeErr := deps.anthropicPrelude.prepare(actx, deps.sink, deps.preludeBuf, translate.FormatAnthropic, deps.env.Stream(), reconnectNoticeForAttempt(actx, deps.request.Header, targetNotice))
			if noticeErr != nil {
				return func(err error) error { return err }, noticeErr
			}
			anthropicTr := translate.NewAnthropicSSETranslator(attemptSink, d.Model, nil).
				WithLogger(deps.log).
				WithEstimatedInputTokens(deps.feats.Tokens).
				WithRequestHadTools(deps.feats.HasTools).
				WithEscapeNormalize(flags.BoolOr(ctx, flags.KeyEscapeNormalize, deps.svc.escapeNormalize)).
				WithToolValidator(deps.toolValidator)
			if err := anthropicTr.Prelude(deps.env.Stream()); err != nil {
				return func(err error) error { return err }, fmt.Errorf("emit Anthropic routing marker: %w", err)
			}
			if deps.preludeBuf != nil {
				deps.preludeBuf.Seal()
			}
			geminiTr := translate.NewGeminiToOpenAISSETranslator(anthropicTr, d.Model, usage)
			anthropicTr.SetUsageSource(geminiTr.UsageFacts)
			rawErr := p.Proxy(actx, d, pr, deps.streamCut.attach(geminiTr), deps.request)
			finalize := func(err error) error {
				if err != nil && deps.env.Stream() && deps.preludeBuf.Committed() {
					deps.streamCut.noteCut(err)
					err = emitAnthropicSSEErrorEvent(deps.sink, attributeStreamError(actx, d.Provider, err))
				}
				err = finalizeAfterProxy(err, geminiTr.Finalize)
				finErr := finalizeAfterProxy(err, anthropicTr.Finalize)
				*deps.respSummary = anthropicTr.Summary()
				return finErr
			}
			return finalize, rawErr
		}
		return func(actx context.Context, d router.Decision, p dispatch.Upstream) error {
			retry := geminiToolModeAttempt{log: deps.log, buffer: deps.preludeBuf, env: deps.env, headers: deps.request.Header, opts: targetOpts}
			return retry.dispatch(prep, func(prepared providers.PreparedRequest) (func(error) error, error) {
				*deps.reqStats = prepared.Stats
				logUpstreamBody(deps.log, deps.routeRes.SessionKey, d, deps.feats, prepared.Body)
				return dispatchGemini(actx, d, p, prepared)
			})
		}, nil
	default:
		return nil, fmt.Errorf("%w: %s (no translation path defined for inbound Anthropic Messages)", ErrProviderNotConfigured, target.Provider)
	}
}
