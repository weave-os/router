package proxy

import (
	"context"
	"log/slog"
	"net/http"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/translate"
)

// rescueServices is what the rescue chain reaches on the proxy service. The
// list keeps those dependencies explicit until they move behind dispatch ports.
type rescueServices interface {
	dispatchWithFallback(ctx context.Context, in failoverInputs) (winnerIdx int, err error)
	resolveEffort(ctx context.Context, decision router.Decision, caps router.ModelSpec, escalate bool) effortResolution
	resolveCredentials(ctx context.Context, provider, model string, headers http.Header) context.Context
	resolveBindingsForDispatch(ctx context.Context, decision router.Decision) []catalog.ProviderBinding
	subscriptionAlternativeDecisions(ctx context.Context, req router.Request, selected router.Decision) []router.Decision
	runCompactionHandover(ctx context.Context, env *translate.RequestEnvelope, reqHeaders http.Header, decisionModel string, scope router.Request) handoverOutcome
	recordSubscriptionUnavailable(ctx context.Context, provider, model string, attemptErr error)
	subscriptionPoolUnavailable(ctx context.Context, provider, model string) bool
	noteRescueReadmission(ctx context.Context, failed, rescuer router.Decision)
	baselineFor(ctx context.Context, requested string) string
	excludedProvidersForRequest(ctx context.Context) map[string]struct{}
	shouldFailover(ctx context.Context) bool
	anthropicFallbackKeyAvailable(ctx context.Context) bool
	claudeSubscriptionExhausted(ctx context.Context, headers http.Header) bool
	siblingFailoverDecisions(ctx context.Context, failed router.Decision, est, sigSavings, outputReserve int) []router.Decision
	overflowRescueDecisions(ctx context.Context, failed router.Decision, est, sigSavings, outputReserve int) []router.Decision
	siblingRescueAllowed(ctx context.Context, gatewayHeld bool) bool
	gatewaySiblingAllowed(ctx context.Context, sibling router.Decision) bool
	subscriptionSiblingAllowed(ctx context.Context) bool
	earlyStopRedoMode(ctx context.Context, routeRes turnLoopResult, decision router.Decision, streaming, agentShadow bool) flags.EarlyStopRedoMode
	earlyStopRedoTarget(ctx context.Context, served router.Decision, est, sigSavings, outputReserve int) (router.Decision, bool)
	redoEarlyStop(ctx context.Context, redo earlyStopRedo) (redone bool, err error)
}

// rescueChain is one turn's walk from the routed attempt through its rescues.
// It owns the served decision, bindings, marker and error while the walk runs;
// the entrypoint reads them back afterwards.
type rescueChain struct {
	svc          rescueServices
	r            *http.Request
	log          *slog.Logger
	env          *translate.RequestEnvelope
	routeRes     turnLoopResult
	req          *router.Request
	opts         translate.EmitOptions
	contentSink  http.ResponseWriter
	preludeBuf   *preludeBuffer
	attempt      dispatchAttempt
	buildAttempt func(target router.Decision, opts translate.EmitOptions, marker string, fundingCtx context.Context) (dispatchAttempt, error)
	flushErr     func(w http.ResponseWriter, err error)
	surface      inference.Purpose
	// Shared with the entrypoint's attempt closures, which write them too.
	respSummary *translate.ResponseSummary
	reqStats    *providers.RequestMutationStats

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

func (c *rescueChain) holdPrimary() {
	c.primaryProvider = c.decision.Provider
	// Captured before rescue: failover replaces decision.Model, so afterwards
	// decision.Model names the rescuer rather than what failed.
	c.primaryModel = c.decision.Model
	c.primaryDecision = c.decision
	// The routed badge, kept for subscription-state targets that land back on
	// the original pick after a sibling failed.
	c.pickedMarker = c.marker
	c.subscriptionPoolFailure = false
}

func (c *rescueChain) dispatchPrimaryAttempt(ctx context.Context, deferFlush bool, onPaidTarget func(router.Decision)) {
	s := c.svc
	r := c.r
	routeRes := c.routeRes
	opts := c.opts
	preludeBuf := c.preludeBuf
	c.winnerIdx, c.proxyErr = s.dispatchWithFallback(ctx, failoverInputs{
		// contentSink is the raw w when capture is off.
		w:                               c.contentSink,
		buf:                             preludeBuf,
		initialDecision:                 c.decision,
		subscriptionStateRequest:        c.req,
		subscriptionStateHeaders:        r.Header,
		subscriptionStateWinnerProvider: &c.subscriptionStateWinnerProvider,
		alternatives: func() []router.Decision {
			if routeRes.HardPinned || routeRes.AuthoritativePerTurn {
				return nil
			}
			return s.subscriptionAlternativeDecisions(ctx, *c.req, c.decision)
		}(),
		buildAlternative: func(target router.Decision) (dispatchAttempt, error) {
			alternativeOpts := opts
			alternativeOpts.TargetModel = target.Model
			alternativeOpts.TargetProvider = target.Provider
			alternativeOpts.Capabilities = router.Lookup(target.Model)
			alternativeOpts.ModelSwitched = true
			alternativeEffort := s.resolveEffort(ctx, target, alternativeOpts.Capabilities, routeRes.EscalateEffort)
			alternativeEffort.apply(&alternativeOpts)
			alternativeMarker := subscriptionTargetMarker(ctx, r.Header, routeRes, c.primaryDecision, target, c.pickedMarker)
			alternativeCtx := s.resolveCredentials(ctx, target.Provider, target.Model, r.Header)
			return c.buildAttempt(target, alternativeOpts, alternativeMarker, alternativeCtx)
		},
		onAlternative: func(target router.Decision) {
			overloadedModel := c.decision.Model
			c.decision = target
			c.bindings = s.resolveBindingsForDispatch(ctx, target)
			c.marker = siblingMarkerFor(ctx, r.Header, routeRes, overloadedModel, target.Model)
		},
		onSubscriptionStateTarget: func(target router.Decision, targetBindings []catalog.ProviderBinding) {
			c.marker = subscriptionTargetMarker(ctx, r.Header, routeRes, c.primaryDecision, target, c.pickedMarker)
			c.decision = target
			c.bindings = targetBindings
		},
		onSubscriptionStatePaidTarget: func(target router.Decision, _ []catalog.ProviderBinding) {
			c.subscriptionStatePaidTargetAttempted = true
			onPaidTarget(target)
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
	s := c.svc
	preludeBuf := c.preludeBuf
	c.primaryFailureErr = c.proxyErr
	c.primarySubscriptionArmFailure = c.proxyErr
	if (servedOnSubscription(ctx) || c.subscriptionOnlySibling) && !preludeBuf.Committed() {
		s.recordSubscriptionUnavailable(ctx, c.decision.Provider, c.decision.Model, c.proxyErr)
	}
}

func (c *rescueChain) siblingRescue(ctx context.Context) {
	s := c.svc
	routeRes := c.routeRes
	preludeBuf := c.preludeBuf
	// Same-cluster failover: all bindings exhausted pre-commit — re-dispatch
	// the policy candidates in order until one serves. Last in the rescue chain.
	c.siblingFailoverUsed = false
	c.siblingRescueRan = false
	// The decision and error the rescue replaced; the post-turn demotion hook
	// strikes the primary, never the rescuer.
	// Keyed off the flush, not off whether an earlier rescue ran: a failed
	// subscription retry keeps the same dark model, so a cluster peer can still
	// serve — but only before the deferred error reaches the wire.
	// Recorded after the plan attempt and its same-model paid retry, so a 5xx on
	// either is what opens the plan-only sibling walk.
	if (servedOnSubscription(ctx) || c.subscriptionOnlySibling) && !preludeBuf.Committed() {
		s.recordSubscriptionUnavailable(ctx, c.decision.Provider, c.decision.Model, c.proxyErr)
	}
	// A gateway overflow walks only the other gateways; same-gateway peers share the window that rejected it.
	overflowRescued := c.overflowRescueViable && isUpstreamContextOverflow(c.proxyErr)
	rescueOrder := c.siblingDecisions
	if overflowRescued {
		rescueOrder = c.overflowRescue
	}
	c.siblingRescueOwed = (c.siblingViable || overflowRescued) && !c.baselineAttempted && !c.deferredErrFlushed &&
		!(c.explicitRequestedModel && isSubscriptionPoolError(c.proxyErr)) &&
		(!c.subscriptionOnlySibling || s.subscriptionPoolUnavailable(ctx, c.decision.Provider, c.decision.Model))
	if c.siblingRescueOwed && c.proxyErr != nil && !preludeBuf.Committed() &&
		(providers.IsRetryable(c.proxyErr) ||
			providers.IsUpstreamModelNotFound(c.proxyErr) ||
			providers.IsUpstreamProviderBillingBlocked(c.proxyErr) ||
			isSubscriptionPoolError(c.proxyErr) ||
			c.crossBindingRejected ||
			rescuesCompactionOverflow(routeRes.TurnType, c.proxyErr) ||
			overflowRescued) {
		for _, siblingDecision := range rescueOrder {
			// A rescuer that itself failed pre-commit hands the turn to the next
			// candidate; a served, committed, or client-cancelled turn ends the walk.
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
	opts := c.opts
	preludeBuf := c.preludeBuf
	siblingOpts := opts
	siblingOpts.TargetModel = siblingDecision.Model
	siblingOpts.TargetProvider = siblingDecision.Provider
	siblingOpts.Capabilities = router.Lookup(siblingDecision.Model)
	// The turn now serves a model the session hasn't seen, so signed
	// thinking blocks from the prior model must not be replayed verbatim.
	siblingOpts.ModelSwitched = true
	siblingEffort := s.resolveEffort(ctx, siblingDecision, siblingOpts.Capabilities, routeRes.EscalateEffort)
	siblingEffort.apply(&siblingOpts)
	siblingCtx := s.resolveCredentials(subscriptionStatePaidRescueContext(rescueCredentialContext(ctx, siblingDecision.Provider), siblingDecision.Model), siblingDecision.Provider, siblingDecision.Model, r.Header)
	siblingOpts.FastMode = fastModeForAttempt(siblingCtx, siblingDecision.Model, siblingDecision.Provider)
	siblingBindings := s.resolveBindingsForDispatch(siblingCtx, siblingDecision)
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
	s.noteRescueReadmission(ctx, c.decision, siblingDecision)
	c.effortServed = siblingEffort
	*c.respSummary = translate.ResponseSummary{}
	*c.reqStats = providers.RequestMutationStats{}
	c.winnerIdx, c.proxyErr = s.dispatchWithFallback(siblingCtx, failoverInputs{
		w:                      c.contentSink,
		buf:                    preludeBuf,
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
