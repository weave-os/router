package proxy

import (
	"context"
	"log/slog"
	"net/http"
	"time"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/conversation"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/routingpolicy"
	"weave-os/router/internal/subscriptions"
	"weave-os/router/internal/telemetry"
	"weave-os/router/internal/telemetry/capture"
	"weave-os/router/internal/telemetry/respobserve"
	"weave-os/router/internal/telemetry/spans"
	"weave-os/router/internal/translate"
	"weave-os/router/internal/translate/toolcheck"
	providerusage "weave-os/router/internal/usage"

	"go.opentelemetry.io/otel/trace"
)

type messagesTurnDispatch struct {
	s                      *Service
	w                      http.ResponseWriter
	r                      *http.Request
	body                   []byte
	requestStart           time.Time
	facts                  requestcontext.RequestFacts
	rateLimit              *dispatch.ThrottleLedger
	log                    *slog.Logger
	env                    *translate.RequestEnvelope
	sessionKey             [sessionpin.SessionKeyLen]byte
	feats                  translate.RoutingFeatures
	promptText             string
	embedInput             string
	strategy               router.Strategy
	taskArm                taskArmTurn
	inboundLastUser        translate.LastUserMessageInfo
	inboundUserPrompt      bool
	inboundLatestToolCalls []byte
	inboundSpiralSignals   conversation.SpiralSignals
	inboundSpiralReasons   []conversation.SpiralReason
	inboundSpiralComputed  bool
	req                    router.Request
	routeRes               routingpolicy.TurnRoute
	routeMs                int64
	decision               router.Decision
	tt                     turntype.TurnType
	stickyHit              bool
	pinAgeSec              int64
	reqPricing             catalog.Pricing
	actPricing             catalog.Pricing
	effortServed           dispatch.EffortResolution
	opts                   translate.EmitOptions
	replayedModel          string
	footerEchoed           bool
	agentShadowMode        bool
	responseBuffer         *responseCostBuffer
	overflowEstimate       int
	outputReserve          int
	escalationCapture      **capture.Writer

	bindings                []catalog.ProviderBinding
	streamCost              *streamCostWriter
	contentSink             http.ResponseWriter
	contentCap              *capture.Writer
	policyOutcomeCap        *capture.Writer
	preludeBuf              *preludeBuffer
	sink                    http.ResponseWriter
	refusalObs              *respobserve.RefusalObserver
	toolUseObs              *respobserve.ToolUseObserver
	earlyStop               *dispatch.EarlyStopGate
	streamCut               *dispatch.StreamCutObserver
	proxyStart              time.Time
	inferenceParentCtx      context.Context
	inferenceSpan           trace.Span
	proxyErr                error
	crossFormat             bool
	extractor               *telemetry.UsageExtractor
	respSummary             translate.ResponseSummary
	reqStats                providers.RequestMutationStats
	marker                  string
	toolValidator           *toolcheck.Validator
	setExtractor            func(e *telemetry.UsageExtractor)
	anthropicPrelude        *requestNoticeStream
	fastServed              bool
	setStreamCost           func(d router.Decision, inputIncludesCache bool, requestCacheTTL1h func() bool)
	anthropicTierAttemptFor func(targetOpts translate.EmitOptions, prep providers.PreparedRequest, targetNotice translate.RouterNotice) *anthropicTierAttempt
	recordFastServed        func(fast bool)
	attemptDeps             messagesAttemptDeps
	rescue                  *dispatch.MessagesRescue
	winnerIdx               int
	finalProvider           string
	proxyMs                 int64
}

func dispatchMessagesTurn(ctx, credentialCtx context.Context, t *messagesTurnDispatch) (context.Context, error) {
	ctx, err := t.bindAndGate(ctx)
	if err != nil {
		return ctx, err
	}
	clientSink := t.w
	if t.env.Stream() && !t.agentShadowMode {
		var closeKeepalive func()
		clientSink, t.streamCost, closeKeepalive = anthropicStreamSink(t.w, t.s.ingress.Keepalive, func() string {
			return t.s.feedbackFooter(ctx, requestcontext.ClientIdentityFrom(ctx).ClientApp, t.routeRes.TurnType, t.footerEchoed)
		})
		defer closeKeepalive()
	}
	t.prepareSinks(ctx, clientSink)
	t.proxyStart = t.s.clock.Now()
	t.inferenceParentCtx = ctx
	ctx, t.inferenceSpan = spans.StartInferenceSpan(ctx, t.decision, clientSessionIDForRequest(ctx, t.env))
	defer t.inferenceSpan.End()
	fundingCtx := t.prepareAttempt(ctx, credentialCtx)
	ctx, err = t.dispatchAttempts(ctx, fundingCtx)
	if err != nil {
		return ctx, err
	}
	ctx = t.settle(ctx)
	return ctx, t.finishTurn(ctx)
}

func (t *messagesTurnDispatch) bindAndGate(ctx context.Context) (context.Context, error) {
	t.bindings = t.s.resolveBindingsForDispatch(ctx, t.decision)

	if billing.SubscriptionOnlyFromContext(ctx) && !t.routeRes.UsageBypass && !subscriptionStateModelsEnabled(ctx) {
		switch {
		case subscriptionOnlyUnservable(ctx, t.decision.Provider, t.decision.Model):
			released, ok := releaseUnservableLinkedFirst(ctx, t.decision)
			if !ok {
				t.log.Info("Subscription-only request cannot be served on the subscription; refusing",
					"requested_model", t.feats.Model, "external_id", t.facts.ExternalID, "decision_provider", t.decision.Provider)
				return ctx, credentials.SubscriptionOnlyUnavailable(ctx)
			}
			ctx = released
		case !credentials.ServedOnCustomerKey(ctx) && t.s.anthropicSubscriptionObservedExhausted(ctx, t.r.Header) && !credentials.ClaudeSubscriptionSuppressed(ctx):
			t.log.Info("Subscription-only request cannot be served on the subscription; refusing",
				"requested_model", t.feats.Model, "external_id", t.facts.ExternalID, "decision_provider", t.decision.Provider)
			return ctx, credentials.SubscriptionOnlyUnavailable(ctx)
		default:
			t.bindings = []catalog.ProviderBinding{{Provider: t.decision.Provider}}
		}
	}
	return ctx, nil
}

func (t *messagesTurnDispatch) prepareSinks(ctx context.Context, clientSink http.ResponseWriter) {
	clientSink, *t.escalationCapture = t.s.captureEscalationResponse(clientSink, t.routeRes)
	t.contentSink, t.contentCap = t.s.maybeCaptureResponse(ctx, clientSink)
	if !t.agentShadowMode {
		t.contentSink, t.policyOutcomeCap = t.s.capturePolicyOutcomeResponse(ctx, t.contentSink, t.routeRes, t.decision)
	}
	t.preludeBuf = newPreludeBuffer(t.contentSink)
	var rootSink http.ResponseWriter = t.preludeBuf
	t.sink = rootSink
	t.refusalObs = respobserve.NewRefusalObserver(t.sink)
	t.sink = t.refusalObs
	t.toolUseObs = respobserve.NewToolUseObserver(t.sink)
	t.sink = t.toolUseObs
	t.earlyStop = dispatch.NewEarlyStopGate(t.sink, t.log)
	t.sink = t.earlyStop
	t.preludeBuf.OnDiscard = t.earlyStop.ResetAttempt
	t.streamCut = dispatch.NewStreamCutObserver(t.s.clock.Now)
	t.streamCut.DescribeRequest(len(t.body), t.env)
}

func (t *messagesTurnDispatch) prepareAttempt(ctx, credentialCtx context.Context) context.Context {
	announcementTurn := announcementTurnFor(t.routeRes, t.replayedModel)
	t.marker = suppressMarkerIfRequested(ctx, t.r.Header, modelSelectionMarkerForRequest(t.s.strategyPorts, ctx, announcementTurn, routingMarkerFor(announcementTurn), t.decision.Model, ""))
	muteSlowStartFor(ctx, t.routeRes)
	fundingCtx := ctx
	if credentials.CredentialsFromContext(fundingCtx) == nil {
		fundingCtx = credentialCtx
	}
	notice := routerNoticeForRequest(ctx, t.r.Header, subscriptions.ProviderClaude, t.decision.Model, t.marker)
	notice = addRoutingFundingSource(fundingCtx, seatAdmissionOf(t.s), t.decision.Provider, t.decision.Model, notice)
	t.marker = notice.Text
	t.toolValidator = t.env.ToolValidator()
	t.setExtractor = func(e *telemetry.UsageExtractor) { t.extractor = e }
	t.anthropicPrelude = &requestNoticeStream{now: t.s.clock.Now}
	t.setStreamCost = func(d router.Decision, inputIncludesCache bool, requestCacheTTL1h func() bool) {
		if t.streamCost != nil {
			t.streamCost.SetCostCalculator(routerCostCalculatorFor(d.Model, d.Provider, t.fastServed), inputIncludesCache, requestCacheTTL1h)
			t.streamCost.source = func() providerusage.Facts { return t.extractor.UsageFacts() }
		}
	}
	t.anthropicTierAttemptFor = func(targetOpts translate.EmitOptions, prep providers.PreparedRequest, targetNotice translate.RouterNotice) *anthropicTierAttempt {
		return &anthropicTierAttempt{
			s:             t.s,
			log:           t.log,
			env:           t.env,
			r:             t.r,
			opts:          targetOpts,
			native:        t.s.anthropicNativeAttempt(t.env, t.r, prep, t.sink, t.preludeBuf, t.anthropicPrelude, t.streamCut, targetNotice, t.setExtractor, t.setStreamCost),
			sink:          t.sink,
			preludeBuf:    t.preludeBuf,
			preludeState:  t.anthropicPrelude,
			streamCut:     t.streamCut,
			notice:        targetNotice,
			setExtractor:  t.setExtractor,
			setStreamCost: t.setStreamCost,
			logBody: func(d router.Decision, body []byte) {
				logUpstreamBody(t.log, t.routeRes.SessionKey, d, t.feats, body)
			},
		}
	}
	t.recordFastServed = func(fast bool) { t.fastServed = fast }

	t.attemptDeps = messagesAttemptDeps{
		request:                 t.r,
		svc:                     t.s,
		env:                     t.env,
		routeRes:                &t.routeRes,
		log:                     t.log,
		crossFormat:             &t.crossFormat,
		feats:                   t.feats,
		anthropicTierAttemptFor: t.anthropicTierAttemptFor,
		recordFastServed:        t.recordFastServed,
		preludeBuf:              t.preludeBuf,
		sink:                    t.sink,
		anthropicPrelude:        t.anthropicPrelude,
		streamCut:               t.streamCut,
		setExtractor:            t.setExtractor,
		setStreamCost:           t.setStreamCost,
		fastServed:              &t.fastServed,
		respSummary:             &t.respSummary,
		reqStats:                &t.reqStats,
		streamCost:              t.streamCost,
		extractor:               &t.extractor,
		toolValidator:           t.toolValidator,
	}
	return fundingCtx
}

func (t *messagesTurnDispatch) dispatchAttempts(ctx, fundingCtx context.Context) (context.Context, error) {
	buildAttempt := func(target router.Decision, targetOpts translate.EmitOptions, targetMarker string, fundingCtx context.Context) (dispatch.UpstreamAttempt, error) {
		return buildMessagesAttempt(ctx, t.attemptDeps, target, targetOpts, targetMarker, fundingCtx)
	}
	attempt, attemptBuildErr := buildAttempt(t.decision, t.opts, t.marker, fundingCtx)
	if attemptBuildErr != nil && !translate.IsIntrinsicallyIncompatible(attemptBuildErr) {
		spans.FinishInferenceSpan(ctx, t.inferenceSpan, t.decision, t.decision.Provider, -1, attemptBuildErr)
		return ctx, attemptBuildErr
	}

	t.rescue = &dispatch.MessagesRescue{
		RescueChain: &dispatch.RescueChain{
			RescuePorts: rescuePortsFor(t.s), Markers: rescueMarkers{t.s.strategyPorts, t.r.Header, t.routeRes, t.s.credentials.UsageObserver},
			HTTPRequest:      t.r,
			Log:              t.log,
			Env:              t.env,
			RouteRes:         rescueTurnOf(t.routeRes),
			Request:          &t.req,
			Opts:             t.opts,
			ContentSink:      t.contentSink,
			PreludeBuf:       t.preludeBuf,
			Attempt:          attempt,
			BuildAttempt:     buildAttempt,
			Surface:          inference.PurposeAnthropicMessages,
			RespSummary:      &t.respSummary,
			ReqStats:         &t.reqStats,
			Decision:         t.decision,
			Bindings:         t.bindings,
			Marker:           t.marker,
			ProxyErr:         t.proxyErr,
			EffortServed:     t.effortServed,
			OverflowEstimate: t.overflowEstimate,
			OutputReserve:    t.outputReserve,
		},
		Feats:           t.feats,
		AgentShadowMode: t.agentShadowMode,

		EarlyStop:            t.earlyStop,
		NoteStreamCut:        t.streamCut.NoteCut,
		AttemptBuildErr:      attemptBuildErr,
		AnthropicTierAttempt: boundTierAttempt(t.anthropicTierAttemptFor, t.recordFastServed),
		Extractor:            &t.extractor,
		FastServed:           &t.fastServed,
		CrossFormat:          &t.crossFormat,
	}
	t.rescue.FlushErr = t.rescue.FlushErrAsAnthropic
	t.rescue.ArmBaseline(ctx)
	t.rescue.ArmSubscriptionRetry(ctx)
	ctx = t.rescue.ArmRescues(ctx)
	t.rescue.ArmEarlyStop(ctx)
	t.rescue.HoldPrimary()
	t.rescue.DispatchPrimary(ctx)
	t.rescue.RedoIfStoppedEarly(ctx)
	t.rescue.NotePrimaryFailure(ctx)
	t.rescue.ClassifyPrimaryFailure()
	ctx = t.rescue.BaselineRescue(ctx)
	t.rescue.SubscriptionRetry(ctx)
	t.rescue.SiblingRescue(ctx)
	t.rescue.Finish(ctx)
	t.decision, t.marker, t.proxyErr, t.effortServed = t.rescue.Decision, t.rescue.Marker, t.rescue.ProxyErr, t.rescue.EffortServed
	t.winnerIdx, t.finalProvider = t.rescue.WinnerIdx, t.rescue.FinalProvider
	return ctx, nil
}

func (t *messagesTurnDispatch) settle(ctx context.Context) context.Context {
	if t.rescue.SubscriptionFailoverUsed {
		ctx = credentials.WithSuppressedClaudeSubscription(ctx)
	}
	ctx = credentials.ResolveAndInjectCredentials(ctx, t.finalProvider, t.decision.Model, t.r.Header)

	if t.extractor.Speed() == catalog.SpeedFast {
		t.fastServed = true
	}
	if actBindingPricing, ok := servedPricing(t.finalProvider, t.decision.Model, t.fastServed); ok {
		t.actPricing = actBindingPricing
	}

	t.proxyMs = t.s.clock.Now().Sub(t.proxyStart).Milliseconds()
	spans.FinishInferenceSpan(ctx, t.inferenceSpan, t.decision, t.finalProvider, t.winnerIdx, t.proxyErr)
	ctx = spans.RestoreParentSpan(ctx, t.inferenceParentCtx)
	return ctx
}

func (t *messagesTurnDispatch) finishTurn(ctx context.Context) error {
	return finishMessagesTurn(ctx, t.s, &messagesFinishedTurn{
		w: t.w, body: t.body, facts: t.facts, requestStart: t.requestStart, rateLimit: t.rateLimit, log: t.log,
		env: t.env, responseBuffer: t.responseBuffer, agentShadowMode: t.agentShadowMode, sessionKey: t.sessionKey, feats: t.feats, promptText: t.promptText,
		embedInput: t.embedInput, strategy: t.strategy, taskArm: t.taskArm, inboundSpiralSignals: t.inboundSpiralSignals, inboundSpiralReasons: t.inboundSpiralReasons, inboundSpiralComputed: t.inboundSpiralComputed,
		inboundLastUser: t.inboundLastUser, inboundUserPrompt: t.inboundUserPrompt, inboundLatestToolCalls: t.inboundLatestToolCalls, overflowEstimate: t.overflowEstimate, req: t.req, routeRes: t.routeRes,
		decision: t.decision, tt: t.tt, stickyHit: t.stickyHit, pinAgeSec: t.pinAgeSec, routeMs: t.routeMs,
		reqPricing: t.reqPricing, actPricing: t.actPricing, opts: t.opts, effortServed: t.effortServed, contentCap: t.contentCap, policyOutcomeCap: t.policyOutcomeCap,
		preludeBuf: t.preludeBuf, refusalObs: t.refusalObs, toolUseObs: t.toolUseObs, streamCut: t.streamCut, proxyStart: t.proxyStart, proxyErr: t.proxyErr,
		crossFormat: t.crossFormat, extractor: t.extractor, respSummary: t.respSummary, reqStats: t.reqStats, marker: t.marker, fastServed: t.fastServed,
		rescue: t.rescue, winnerIdx: t.winnerIdx, finalProvider: t.finalProvider, proxyMs: t.proxyMs,
	})
}
