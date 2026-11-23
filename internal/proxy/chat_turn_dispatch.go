package proxy

import (
	"context"
	"log/slog"
	"net/http"
	"time"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/routingpolicy"
	"weave-os/router/internal/telemetry"
	"weave-os/router/internal/translate"
	"weave-os/router/internal/translate/toolcheck"

	"go.opentelemetry.io/otel/trace"
)

type chatTurnDispatch struct {
	s                       *Service
	w                       http.ResponseWriter
	r                       *http.Request
	body                    []byte
	requestStart            time.Time
	facts                   requestcontext.RequestFacts
	rateLimit               *dispatch.ThrottleLedger
	log                     *slog.Logger
	env                     *translate.RequestEnvelope
	sessionKey              [sessionpin.SessionKeyLen]byte
	feats                   translate.RoutingFeatures
	promptText              string
	embedInput              string
	strategy                router.Strategy
	taskArm                 taskArmTurn
	inboundLastUser         translate.LastUserMessageInfo
	inboundUserPromptOpenAI bool
	routeRequest            router.Request
	routeRes                routingpolicy.TurnRoute
	routeMs                 int64
	decision                router.Decision
	tt                      turntype.TurnType
	stickyHit               bool
	pinTier                 string
	pinAgeSec               int64
	reqPricing              catalog.Pricing
	actPricing              catalog.Pricing
	effortServed            dispatch.EffortResolution
	opts                    translate.EmitOptions
	replayedModel           string
	footerEchoed            bool
	responsesBody           []byte
	openAIResponsesEndpoint bool
	isResponsesWriter       bool
	overflowEstimateOAI     int
	outputReserveOAI        int
	responsesEndpointKey    string
	escalationCapture       **captureWriter

	bindings             []catalog.ProviderBinding
	contentSink          http.ResponseWriter
	contentCap           *captureWriter
	marker               string
	responsesWriter      *translate.ResponsesWriter
	isResponses          bool
	responseSwitch       *responseProtocolSwitch
	responsesNotices     *requestNoticeStream
	responsesPreludeBuf  *preludeBuffer
	preludeBuf           *preludeBuffer
	sink                 http.ResponseWriter
	makeMarkerSink       func(notice translate.RouterNotice) (http.ResponseWriter, error)
	toolValidator        *toolcheck.Validator
	proxyStart           time.Time
	inferenceParentCtx   context.Context
	inferenceSpan        trace.Span
	proxyErr             error
	crossFormat          bool
	extractor            *telemetry.UsageExtractor
	respSummary          translate.ResponseSummary
	nativeRespSummary    bool
	cyberRetryArmed      bool
	preOutputRescueArmed bool
	cyberRefusalSeen     bool
	attempt              dispatch.UpstreamAttempt
	buildAttempt         modelAttemptBuilder
	rescue               *dispatch.ChatRescue
	winnerIdx            int
	finalProvider        string
	fastServed           bool
}

func dispatchChatTurn(ctx, credentialCtx context.Context, t *chatTurnDispatch) (context.Context, error) {
	ctx, err := t.bindAndGate(ctx)
	if err != nil {
		return ctx, err
	}
	fundingCtx, err := t.prepareSinks(ctx, credentialCtx)
	if err != nil {
		return ctx, err
	}
	t.proxyStart = t.s.clock.Now()
	t.inferenceParentCtx = ctx
	ctx, t.inferenceSpan = startInferenceSpan(ctx, t.decision, clientSessionIDForRequest(ctx, t.env))
	defer t.inferenceSpan.End()
	ctx, err = t.dispatchAttempts(ctx, fundingCtx)
	if err != nil {
		return ctx, err
	}
	return t.finish(ctx)
}

func (t *chatTurnDispatch) bindAndGate(ctx context.Context) (context.Context, error) {
	t.bindings = t.s.resolveBindingsForDispatch(ctx, t.decision)

	if billing.SubscriptionOnlyFromContext(ctx) && !subscriptionStateModelsEnabled(ctx) {
		if subscriptionOnlyUnservable(ctx, t.decision.Provider, t.decision.Model) {
			released, ok := releaseUnservableLinkedFirst(ctx, t.decision)
			if !ok {
				t.log.Info("Subscription-only request cannot be served on the subscription; refusing",
					"requested_model", t.feats.Model, "external_id", t.facts.ExternalID, "decision_provider", t.decision.Provider)
				return ctx, credentials.SubscriptionOnlyUnavailable(ctx)
			}
			ctx = released
		} else {
			t.bindings = []catalog.ProviderBinding{{Provider: t.decision.Provider}}
		}
	}
	return ctx, nil
}

func (t *chatTurnDispatch) prepareSinks(ctx, credentialCtx context.Context) (context.Context, error) {
	clientSink := t.w
	if _, isResponses := t.w.(*translate.ResponsesWriter); t.env.Stream() && !isResponses {
		if footer := t.s.feedbackFooter(ctx, requestcontext.ClientIdentityFrom(ctx).ClientApp, t.routeRes.TurnType, t.footerEchoed); footer != "" {
			clientSink = translate.NewOpenAIRoutingFooterWriter(t.w, footer)
		}
	}
	clientSink, *t.escalationCapture = t.s.captureEscalationResponse(clientSink, t.routeRes)
	t.contentSink, t.contentCap = t.s.maybeCaptureResponse(ctx, clientSink)

	announcementTurn := announcementTurnFor(t.routeRes, t.replayedModel)
	if announcedModel, ok := ctx.Value(responsesLastAnnouncedModelContextKey{}).(string); ok {
		announcementTurn.PriorServedModel = announcedModel
	}
	t.marker = suppressMarkerIfRequested(ctx, t.r.Header, modelSelectionMarkerForRequest(t.s.strategyPorts, ctx, announcementTurn, routingMarkerFor(announcementTurn), t.decision.Model, ""))
	muteSlowStartFor(ctx, t.routeRes)
	fundingCtx := ctx
	if CredentialsFromContext(fundingCtx) == nil {
		fundingCtx = credentialCtx
	}
	notice := routerNoticeForDecision(ctx, t.r.Header, t.decision, t.marker)
	notice = addRoutingFundingSource(fundingCtx, seatAdmissionOf(t.s), t.decision.Provider, t.decision.Model, notice)
	t.marker = notice.Text

	prelude, err := prepareChatResponsesPrelude(ctx, chatResponsesPreludeDeps{
		svc: t.s, responseWriter: t.w, contentSink: t.contentSink, responsesBody: t.responsesBody, openAIResponsesEndpoint: t.openAIResponsesEndpoint,
		handoverInvoked: t.routeRes.Handover.Invoked, turnType: t.routeRes.TurnType, decision: t.decision, facts: t.facts, footerEchoed: t.footerEchoed,
		bindingCount: len(t.bindings), marker: t.marker, notice: notice, stream: t.env.Stream(), log: t.log,
	})
	if err != nil {
		return nil, err
	}
	t.responsesBody, t.responsesWriter, t.isResponses, t.responseSwitch = prelude.responsesBody, prelude.responsesWriter, prelude.isResponses, prelude.responseSwitch
	t.responsesNotices, t.responsesPreludeBuf, t.preludeBuf, t.sink, t.makeMarkerSink = prelude.responsesNotices, prelude.responsesPreludeBuf, prelude.preludeBuf, prelude.sink, prelude.makeMarkerSink

	t.toolValidator = t.env.ToolValidator()
	return fundingCtx, nil
}

func (t *chatTurnDispatch) dispatchAttempts(ctx, fundingCtx context.Context) (context.Context, error) {
	attemptDeps := chatAttemptDeps{
		request:              t.r,
		svc:                  t.s,
		isResponses:          t.isResponses,
		responseSwitch:       t.responseSwitch,
		facts:                t.facts,
		responsesNotices:     t.responsesNotices,
		responsesPreludeBuf:  t.responsesPreludeBuf,
		env:                  t.env,
		responseWriter:       t.w,
		responsesBody:        t.responsesBody,
		log:                  t.log,
		fastServed:           &t.fastServed,
		routeRes:             &t.routeRes,
		makeMarkerSink:       t.makeMarkerSink,
		cyberRetryArmed:      &t.cyberRetryArmed,
		preOutputRescueArmed: &t.preOutputRescueArmed,
		extractor:            &t.extractor,
		toolValidator:        t.toolValidator,
		responsesWriter:      t.responsesWriter,
		preludeBuf:           t.preludeBuf,
		sink:                 t.sink,
		respSummary:          &t.respSummary,
		nativeRespSummary:    &t.nativeRespSummary,
		cyberRefusalSeen:     &t.cyberRefusalSeen,
		responsesEndpointKey: t.responsesEndpointKey,
		crossFormat:          &t.crossFormat,
	}
	buildProviderAttempt := func(target router.Decision, targetOpts translate.EmitOptions, targetMarker string, fundingCtx context.Context) (dispatch.UpstreamAttempt, error) {
		return buildChatAttempt(ctx, attemptDeps, target, targetOpts, targetMarker, fundingCtx)
	}

	t.buildAttempt = t.responseSwitch.wrapBuilder(buildProviderAttempt)
	var attemptBuildErr error
	t.attempt, attemptBuildErr = t.buildAttempt(t.decision, t.opts, t.marker, fundingCtx)
	if attemptBuildErr != nil {
		finishInferenceSpan(t.inferenceSpan, t.decision, t.decision.Provider, -1, attemptBuildErr)
		return ctx, attemptBuildErr
	}

	surfacePurpose := inference.PurposeOpenAIChatCompletions
	if t.isResponsesWriter {
		surfacePurpose = inference.PurposeOpenAIResponses
	}
	t.rescue = &dispatch.ChatRescue{
		RescueChain: &dispatch.RescueChain{
			RescuePorts: rescuePortsFor(t.s), Markers: rescueMarkers{t.s.strategyPorts, t.r.Header, t.routeRes, t.s.credentials.UsageObserver},
			HTTPRequest:      t.r,
			Log:              t.log,
			Env:              t.env,
			RouteRes:         rescueTurnOf(t.routeRes),
			Request:          &t.routeRequest,
			Opts:             t.opts,
			ContentSink:      t.contentSink,
			PreludeBuf:       t.preludeBuf,
			Attempt:          t.attempt,
			BuildAttempt:     t.buildAttempt,
			Surface:          surfacePurpose,
			OverflowEstimate: t.overflowEstimateOAI,
			OutputReserve:    t.outputReserveOAI,
			RespSummary:      &t.respSummary,
			Decision:         t.decision,
			Bindings:         t.bindings,
			Marker:           t.marker,
			ProxyErr:         t.proxyErr,
			EffortServed:     t.effortServed,
		},
		IsResponses:          t.isResponses,
		ResponsesPreludeBuf:  t.responsesPreludeBuf,
		CyberRefusalSeen:     &t.cyberRefusalSeen,
		CyberRetryArmed:      &t.cyberRetryArmed,
		PreOutputRescueArmed: &t.preOutputRescueArmed,
	}
	t.rescue.FlushErr = t.rescue.FlushErrAsOpenAI
	t.rescue.ArmCyberRetry(ctx, t.strategy)
	t.rescue.ArmSubscriptionRetries(ctx)
	ctx = t.rescue.ArmRescues(ctx)
	t.rescue.HoldPrimary()
	t.rescue.DispatchPrimary(ctx)
	t.rescue.NotePrimaryFailure(ctx)
	t.rescue.CodexRetry(ctx)
	t.rescue.ClaudeRetry(ctx)
	t.rescue.CyberRetry(ctx)
	t.rescue.SiblingRescue(ctx)
	t.rescue.Finish(ctx)
	t.decision, t.marker, t.proxyErr, t.effortServed = t.rescue.Decision, t.rescue.Marker, t.rescue.ProxyErr, t.rescue.EffortServed
	t.winnerIdx, t.finalProvider = t.rescue.WinnerIdx, t.rescue.FinalProvider
	return ctx, nil
}

func (t *chatTurnDispatch) finish(ctx context.Context) (context.Context, error) {
	if t.rescue.CodexFailoverUsed {
		ctx = withSuppressedCodexSubscription(ctx)
	} else if t.rescue.ClaudeFailoverUsed {
		ctx = credentials.WithSuppressedClaudeSubscription(ctx)
	}
	ctx = t.s.resolveCredentials(ctx, t.finalProvider, t.decision.Model, t.r.Header)

	if t.extractor.Speed() == catalog.SpeedFast {
		t.fastServed = true
	}
	if actBindingPricing, ok := servedPricing(t.finalProvider, t.decision.Model, t.fastServed); ok {
		t.actPricing = actBindingPricing
	}

	proxyMs := t.s.clock.Now().Sub(t.proxyStart).Milliseconds()
	finishInferenceSpan(t.inferenceSpan, t.decision, t.finalProvider, t.winnerIdx, t.proxyErr)
	ctx = restoreParentSpan(ctx, t.inferenceParentCtx)

	return ctx, finishChatTurn(ctx, t.s, &chatFinishedTurn{
		w: t.w, body: t.body, requestStart: t.requestStart, facts: t.facts, rateLimit: t.rateLimit, log: t.log, env: t.env, sessionKey: t.sessionKey, feats: t.feats,
		promptText: t.promptText, embedInput: t.embedInput, strategy: t.strategy, taskArm: t.taskArm, inboundLastUser: t.inboundLastUser,
		inboundUserPromptOpenAI: t.inboundUserPromptOpenAI, routeRequest: t.routeRequest, routeRes: t.routeRes, routeMs: t.routeMs, decision: t.decision,
		tt: t.tt, stickyHit: t.stickyHit, pinTier: t.pinTier, pinAgeSec: t.pinAgeSec, reqPricing: t.reqPricing, actPricing: t.actPricing,
		effortServed: t.effortServed, fastServed: t.fastServed, contentCap: t.contentCap, marker: t.marker, responsesPreludeBuf: t.responsesPreludeBuf,
		preludeBuf: t.preludeBuf, proxyStart: t.proxyStart, proxyErr: t.proxyErr, crossFormat: t.crossFormat, extractor: t.extractor,
		respSummary: t.respSummary, nativeRespSummary: t.nativeRespSummary, cyberRefusalSeen: t.cyberRefusalSeen, rescue: t.rescue, winnerIdx: t.winnerIdx,
		finalProvider: t.finalProvider, proxyMs: proxyMs,
	})
}
