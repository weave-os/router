package proxy

import (
	"context"
	"log/slog"
	"net/http"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/translate"
)

type rescueServices interface {
	dispatchWithFallback(ctx context.Context, in failoverInputs) (winnerIdx int, err error)
	resolveEffort(ctx context.Context, decision router.Decision, caps router.ModelSpec, escalate bool) effortResolution
	runCompactionHandover(ctx context.Context, env *translate.RequestEnvelope, reqHeaders http.Header, decisionModel string, scope router.Request) handoverOutcome
	earlyStopRedoMode(ctx context.Context, routeRes turnLoopResult, decision router.Decision, streaming, agentShadow bool) flags.EarlyStopRedoMode
	redoEarlyStop(ctx context.Context, redo earlyStopRedo) (redone bool, err error)
}

type rescueChain struct {
	rescuePorts
	seats            seatAdmission
	clock            auth.Clock
	usageObserver    *usage.Observer
	r                *http.Request
	log              *slog.Logger
	env              *translate.RequestEnvelope
	routeRes         turnLoopResult
	req              *router.Request
	opts             translate.EmitOptions
	contentSink      http.ResponseWriter
	preludeBuf       *preludeBuffer
	attempt          dispatchAttempt
	buildAttempt     func(target router.Decision, opts translate.EmitOptions, marker string, fundingCtx context.Context) (dispatchAttempt, error)
	flushErr         func(w http.ResponseWriter, err error)
	surface          inference.Purpose
	overflowEstimate int
	outputReserve    int
	respSummary      *translate.ResponseSummary
	reqStats         *providers.RequestMutationStats

	decision     router.Decision
	bindings     []catalog.ProviderBinding
	marker       string
	proxyErr     error
	effortServed effortResolution
	winnerIdx    int

	primaryProvider                      string
	primaryModel                         string
	primaryDecision                      router.Decision
	pickedMarker                         string
	subscriptionStateWinnerProvider      string
	subscriptionPoolFailure              bool
	subscriptionStatePaidTargetAttempted bool
	primaryFailureErr                    error
	primarySubscriptionArmFailure        error
	deferredErrFlushed                   bool

	explicitRequestedModel  bool
	siblingDecisions        []router.Decision
	siblingViable           bool
	subscriptionOnlySibling bool
	overflowRescue          []router.Decision
	overflowRescueViable    bool
	rescueArmed             bool
	overflowRescueOnly      bool
	baselineAttempted       bool
	crossBindingRejected    bool

	siblingFailoverUsed bool
	siblingRescueRan    bool
	siblingRescueOwed   bool
	rescuedPrimary      router.Decision
	rescuedPrimaryErr   error

	finalProvider     string
	failoverUsed      bool
	failoverAttempted bool
}

func (c *rescueChain) armSiblings(ctx context.Context, rescueBlocked bool) context.Context {
	env := c.env
	ctx = withClaudeEnterpriseTurn(ctx, c.decision)
	ctx = WithManagedSubscriptionUsage(ctx)
	c.siblingDecisions = subscriptionStatePaidRescueDecisions(ctx, c.routing.SiblingFailoverDecisions(ctx, rescueBasisForTurn(c.decision, c.routeRes), c.overflowEstimate, env.SignatureTokenSavings(), c.outputReserve))
	c.siblingViable = len(c.siblingDecisions) > 0 &&
		!rescueBlocked &&
		c.routing.SiblingRescueAllowed(ctx, c.routing.GatewaySiblingAllowed(ctx, c.siblingDecisions[0]))
	c.subscriptionOnlySibling = c.siblingViable && !c.routing.ShouldFailover(ctx) &&
		!c.routing.GatewaySiblingAllowed(ctx, c.siblingDecisions[0]) && c.credentials.SubscriptionSiblingAllowed(ctx)
	c.overflowRescue = c.routing.OverflowRescueDecisions(ctx, c.decision, c.overflowEstimate, env.SignatureTokenSavings(), c.outputReserve)
	c.overflowRescueViable = len(c.overflowRescue) > 0 &&
		!rescueBlocked &&
		c.routing.SiblingRescueAllowed(ctx, true)
	c.rescueArmed = c.siblingViable || c.overflowRescueViable
	c.overflowRescueOnly = c.overflowRescueViable && !c.siblingViable
	return ctx
}

func (c *rescueChain) holdPrimary() {
	c.primaryProvider = c.decision.Provider
	c.primaryModel = c.decision.Model
	c.primaryDecision = c.decision
	c.pickedMarker = c.marker
}

func (c *rescueChain) dispatchPrimaryAttempt(ctx context.Context, deferFlush bool, onPaidTarget func(router.Decision)) {
	s := c.svc
	r := c.r
	routeRes := c.routeRes
	c.winnerIdx, c.proxyErr = s.dispatchWithFallback(ctx, failoverInputs{
		w:                               c.contentSink,
		buf:                             c.preludeBuf,
		initialDecision:                 c.decision,
		subscriptionStateRequest:        c.translationEligibleRequest(),
		subscriptionStateHeaders:        r.Header,
		subscriptionStateWinnerProvider: &c.subscriptionStateWinnerProvider,
		alternatives: func() []router.Decision {
			if routeRes.HardPinned || routeRes.AuthoritativePerTurn {
				return nil
			}
			return c.credentials.SubscriptionAlternativeDecisions(ctx, *c.req, c.decision)
		}(),
		buildAlternative: func(target router.Decision) (dispatchAttempt, error) {
			alternativeOpts, _ := switchedAttemptOptions(ctx, c.opts, target, routeRes.EscalateEffort, s.resolveEffort)
			alternativeMarker := subscriptionTargetMarker(ctx, r.Header, routeRes, c.primaryDecision, target, c.pickedMarker)
			alternativeCtx := c.credentials.ResolveCredentials(ctx, target.Provider, target.Model, r.Header)
			return c.buildAttempt(target, alternativeOpts, alternativeMarker, alternativeCtx)
		},
		onAlternative: func(target router.Decision) {
			overloadedModel := c.decision.Model
			c.decision = target
			c.bindings = c.routing.ResolveBindingsForDispatch(ctx, target)
			c.marker = siblingMarkerFor(ctx, r.Header, routeRes, overloadedModel, target.Model)
		},
		onSubscriptionStateTarget: func(target router.Decision, targetBindings []catalog.ProviderBinding) {
			c.marker = subscriptionTargetMarker(ctx, r.Header, routeRes, c.primaryDecision, target, c.pickedMarker)
			c.decision = target
			c.bindings = targetBindings
		},
		onSubscriptionStatePaidTarget: func(target router.Decision, _ []catalog.ProviderBinding) {
			c.subscriptionStatePaidTargetAttempted = true
			if onPaidTarget != nil {
				onPaidTarget(target)
			}
		},
		bindings:               c.bindings,
		attempt:                c.attempt,
		flushErr:               c.flushErr,
		deferFlushOnExhaustion: deferFlush,
		purpose:                routeRes.dispatchPurpose(c.surface),
		origin:                 routeRes.dispatchOrigin(c.decision),
	})
	c.subscriptionPoolFailure = isSubscriptionPoolError(c.proxyErr)
}

func (c *rescueChain) notePrimaryFailure(ctx context.Context) {
	c.primaryFailureErr = c.proxyErr
	c.primarySubscriptionArmFailure = c.proxyErr
	if (servedOnSubscription(ctx) || c.subscriptionOnlySibling) && !c.preludeBuf.Committed() {
		c.credentials.RecordSubscriptionUnavailable(ctx, c.decision.Provider, c.decision.Model, c.proxyErr)
	}
}

func (c *rescueChain) siblingRescue(ctx context.Context) {
	preludeBuf := c.preludeBuf
	if (servedOnSubscription(ctx) || c.subscriptionOnlySibling) && !preludeBuf.Committed() {
		c.credentials.RecordSubscriptionUnavailable(ctx, c.decision.Provider, c.decision.Model, c.proxyErr)
	}
	overflowRescued := c.overflowRescueViable && isUpstreamContextOverflow(c.proxyErr)
	rescueOrder := c.siblingDecisions
	if overflowRescued {
		rescueOrder = c.overflowRescue
	}
	c.siblingRescueOwed = (c.siblingViable || overflowRescued) && !c.baselineAttempted && !c.deferredErrFlushed &&
		(!c.explicitRequestedModel || !isSubscriptionPoolError(c.proxyErr)) &&
		(!c.subscriptionOnlySibling || c.credentials.SubscriptionPoolUnavailable(ctx, c.decision.Provider, c.decision.Model))
	if c.siblingRescueOwed && c.proxyErr != nil && !preludeBuf.Committed() &&
		(providers.IsRetryable(c.proxyErr) ||
			providers.IsUpstreamModelNotFound(c.proxyErr) ||
			providers.IsUpstreamProviderBillingBlocked(c.proxyErr) ||
			isSubscriptionPoolError(c.proxyErr) ||
			c.crossBindingRejected ||
			rescuesCompactionOverflow(c.routeRes.TurnType, c.proxyErr) ||
			overflowRescued) {
		for _, siblingDecision := range rescueOrder {
			if c.siblingRescueRan && (c.proxyErr == nil || preludeBuf.Committed() || ctx.Err() != nil) {
				break
			}
			c.dispatchSibling(ctx, siblingDecision)
		}
	}
}

func (c *rescueChain) dispatchSibling(ctx context.Context, siblingDecision router.Decision) {
	s := c.svc
	r := c.r
	log := c.log
	routeRes := c.routeRes
	siblingOpts, siblingEffort := switchedAttemptOptions(ctx, c.opts, siblingDecision, routeRes.EscalateEffort, s.resolveEffort)
	siblingCtx := c.credentials.ResolveCredentials(subscriptionStatePaidRescueContext(rescueCredentialContext(ctx, siblingDecision.Provider), siblingDecision.Model), siblingDecision.Provider, siblingDecision.Model, r.Header)
	siblingOpts.FastMode = fastModeForAttempt(siblingCtx, siblingDecision.Model, siblingDecision.Provider)
	siblingBindings := c.routing.ResolveBindingsForDispatch(siblingCtx, siblingDecision)
	siblingMarker := withRescueDetail(siblingMarkerFor(ctx, r.Header, routeRes, c.decision.Model, siblingDecision.Model), lastFailureDetail(attemptTraceFromContext(ctx)))
	siblingAttempt, siblingBuildErr := c.buildAttempt(siblingDecision, siblingOpts, siblingMarker, siblingCtx)
	if siblingBuildErr != nil {
		log.Error("Sibling failover: preparing the candidate request failed; trying the next candidate",
			"err", siblingBuildErr,
			"sibling_model", siblingDecision.Model)
		return
	}
	if len(siblingBindings) == 0 {
		log.Warn("Sibling failover: candidate has no usable binding; trying the next candidate",
			"sibling_model", siblingDecision.Model,
			"sibling_provider", siblingDecision.Provider,
			"err", c.proxyErr)
		return
	}
	log.Warn("Sibling failover: model exhausted, retrying a same-cluster candidate",
		"failed_model", c.decision.Model,
		"failed_provider", c.decision.Provider,
		"primary_model", c.primaryModel,
		"sibling_model", siblingDecision.Model,
		"sibling_provider", siblingDecision.Provider,
		"upstream_status", upstreamStatus(c.proxyErr),
		"err", c.proxyErr)
	recordTurnFailure(ctx, c.decision.Model)
	if !c.siblingRescueRan {
		c.rescuedPrimary = c.decision
		c.rescuedPrimaryErr = c.proxyErr
	}
	c.siblingRescueRan = true
	c.routing.NoteRescueReadmission(ctx, c.decision, siblingDecision)
	c.effortServed = siblingEffort
	*c.respSummary = translate.ResponseSummary{}
	if c.reqStats != nil {
		*c.reqStats = providers.RequestMutationStats{}
	}
	c.winnerIdx, c.proxyErr = s.dispatchWithFallback(siblingCtx, failoverInputs{
		w:                      c.contentSink,
		buf:                    c.preludeBuf,
		initialDecision:        siblingDecision,
		bindings:               siblingBindings,
		attempt:                siblingAttempt,
		flushErr:               c.flushErr,
		deferFlushOnExhaustion: true,
		purpose:                routeRes.dispatchPurpose(c.surface),
		origin:                 routeRes.rescueOrigin(),
	})
	c.subscriptionPoolFailure = isSubscriptionPoolError(c.proxyErr)
	c.decision = siblingDecision
	c.bindings = siblingBindings
	c.marker = siblingMarker
	c.siblingFailoverUsed = c.proxyErr == nil
}

func (c *rescueChain) servedProvider(fallback string) string {
	if c.subscriptionStateWinnerProvider != "" {
		return c.subscriptionStateWinnerProvider
	} else if c.winnerIdx >= 0 && c.winnerIdx < len(c.bindings) {
		return c.bindings[c.winnerIdx].Provider
	}
	return fallback
}

func (c *rescueChain) translationEligibleRequest() *router.Request {
	if c.routeRes.TranslationEnabledProviders == nil {
		return c.req
	}
	req := *c.req
	req.EnabledProviders = c.routeRes.TranslationEnabledProviders
	return &req
}
