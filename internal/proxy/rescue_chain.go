package proxy

import (
	"context"
	"log/slog"
	"net/http"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/translate"
)

// rescueServices is what the rescue chain still reaches on the proxy service:
// the dispatch and turn steps whose signatures carry proxy types. Credential
// and routing needs go through dispatch.RescueCredentials and
// dispatch.RescueRouting.
type rescueServices interface {
	dispatchWithFallback(ctx context.Context, in dispatch.FailoverInputs) (winnerIdx int, err error)
	resolveEffort(ctx context.Context, decision router.Decision, caps router.ModelSpec, escalate bool) dispatch.EffortResolution
	runCompactionHandover(ctx context.Context, env *translate.RequestEnvelope, reqHeaders http.Header, decisionModel string, scope router.Request) dispatch.HandoverOutcome
	earlyStopRedoMode(ctx context.Context, routeRes turnLoopResult, decision router.Decision, streaming, agentShadow bool) flags.EarlyStopRedoMode
	redoEarlyStop(ctx context.Context, redo dispatch.EarlyStopRedo) (redone bool, err error)
}

// rescueChain is one turn's walk from the routed attempt through its rescues.
// It owns the served decision, bindings, marker and error while the walk runs;
// the entrypoint reads them back afterwards.
type rescueChain struct {
	rescuePorts
	r                *http.Request
	log              *slog.Logger
	env              *translate.RequestEnvelope
	routeRes         turnLoopResult
	req              *router.Request
	opts             translate.EmitOptions
	contentSink      http.ResponseWriter
	preludeBuf       *preludeBuffer
	attempt          dispatch.UpstreamAttempt
	buildAttempt     func(target router.Decision, opts translate.EmitOptions, marker string, fundingCtx context.Context) (dispatch.UpstreamAttempt, error)
	flushErr         func(w http.ResponseWriter, err error)
	surface          inference.Purpose
	overflowEstimate int
	outputReserve    int
	// Shared with the entrypoint's attempt closures, which write them too.
	respSummary *translate.ResponseSummary
	reqStats    *providers.RequestMutationStats

	decision     router.Decision
	bindings     []catalog.ProviderBinding
	marker       string
	proxyErr     error
	effortServed dispatch.EffortResolution
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
	// The decision and error the rescue replaced; the post-turn demotion hook
	// strikes the primary, never the rescuer.
	rescuedPrimary    router.Decision
	rescuedPrimaryErr error

	finalProvider     string
	failoverUsed      bool
	failoverAttempted bool
}

// armSiblings resolves the same-cluster and overflow rescue candidates.
// rescueBlocked carries the entrypoint's own reasons the walk may not run.
func (c *rescueChain) armSiblings(ctx context.Context, rescueBlocked bool) context.Context {
	env := c.env
	// Same-cluster model failover: when the routed model's only binding is dark,
	// degrade to a peer the policy already scored. Gated out for depleted-credit
	// turns (a different model incurs the paid spend that mode forbids). BYOK
	// normally disables failover, but a gateway-aliased sibling uses the same
	// held credentials, so it stays eligible.
	// The plan-capacity mark set during dispatch must be readable here.
	ctx = withClaudeEnterpriseTurn(ctx, c.decision)
	ctx = WithManagedSubscriptionUsage(ctx)
	c.siblingDecisions = subscriptionStatePaidRescueDecisions(ctx, c.routing.SiblingFailoverDecisions(ctx, rescueBasisForTurn(c.decision, c.routeRes), c.overflowEstimate, env.SignatureTokenSavings(), c.outputReserve))
	c.siblingViable = len(c.siblingDecisions) > 0 &&
		!rescueBlocked &&
		c.routing.SiblingRescueAllowed(ctx, c.routing.GatewaySiblingAllowed(ctx, c.siblingDecisions[0]))
	// When the plan credential is the only reason the walk is open, the walk
	// must wait for the plan's own 5xx mark: a 429 or timeout on the plan must
	// not buy a paid sibling.
	c.subscriptionOnlySibling = c.siblingViable && !c.routing.ShouldFailover(ctx) &&
		!c.routing.GatewaySiblingAllowed(ctx, c.siblingDecisions[0]) && c.credentials.SubscriptionSiblingAllowed(ctx)
	c.overflowRescue = c.routing.OverflowRescueDecisions(ctx, c.decision, c.overflowEstimate, env.SignatureTokenSavings(), c.outputReserve)
	c.overflowRescueViable = len(c.overflowRescue) > 0 &&
		!rescueBlocked &&
		c.routing.SiblingRescueAllowed(ctx, true)
	// Either rescue holds the primary's error until it knows whether it runs.
	c.rescueArmed = c.siblingViable || c.overflowRescueViable
	c.overflowRescueOnly = c.overflowRescueViable && !c.siblingViable
	return ctx
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
}

func (c *rescueChain) dispatchPrimaryAttempt(ctx context.Context, deferFlush bool, onPaidTarget func(router.Decision)) {
	s := c.svc
	r := c.r
	routeRes := c.routeRes
	c.winnerIdx, c.proxyErr = s.dispatchWithFallback(ctx, dispatch.FailoverInputs{
		// contentSink is the raw w when capture is off.
		Writer:                          c.contentSink,
		Prelude:                         c.preludeBuf,
		InitialDecision:                 c.decision,
		SubscriptionStateRequest:        c.req,
		SubscriptionStateHeaders:        r.Header,
		SubscriptionStateWinnerProvider: &c.subscriptionStateWinnerProvider,
		Alternatives: func() []router.Decision {
			if routeRes.HardPinned || routeRes.AuthoritativePerTurn {
				return nil
			}
			return c.credentials.SubscriptionAlternativeDecisions(ctx, *c.req, c.decision)
		}(),
		BuildAlternative: func(target router.Decision) (dispatch.UpstreamAttempt, error) {
			alternativeOpts, _ := switchedAttemptOptions(ctx, c.opts, target, routeRes.EscalateEffort, s.resolveEffort)
			alternativeMarker := subscriptionTargetMarker(ctx, r.Header, routeRes, c.primaryDecision, target, c.pickedMarker)
			alternativeCtx := c.credentials.ResolveCredentials(ctx, target.Provider, target.Model, r.Header)
			return c.buildAttempt(target, alternativeOpts, alternativeMarker, alternativeCtx)
		},
		OnAlternative: func(target router.Decision) {
			overloadedModel := c.decision.Model
			c.decision = target
			c.bindings = c.routing.ResolveBindingsForDispatch(ctx, target)
			c.marker = siblingMarkerFor(ctx, r.Header, routeRes, overloadedModel, target.Model)
		},
		OnSubscriptionStateTarget: func(target router.Decision, targetBindings []catalog.ProviderBinding) {
			c.marker = subscriptionTargetMarker(ctx, r.Header, routeRes, c.primaryDecision, target, c.pickedMarker)
			c.decision = target
			c.bindings = targetBindings
		},
		OnSubscriptionStatePaidTarget: func(target router.Decision, _ []catalog.ProviderBinding) {
			c.subscriptionStatePaidTargetAttempted = true
			if onPaidTarget != nil {
				onPaidTarget(target)
			}
		},
		Bindings:               c.bindings,
		Attempt:                c.attempt,
		FlushErr:               c.flushErr,
		DeferFlushOnExhaustion: deferFlush,
		Purpose:                routeRes.dispatchPurpose(c.surface),
		Origin:                 routeRes.dispatchOrigin(c.decision),
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

// siblingRescue is same-cluster failover: all bindings exhausted pre-commit —
// re-dispatch the policy candidates in order until one serves. Last in the
// rescue chain.
func (c *rescueChain) siblingRescue(ctx context.Context) {
	preludeBuf := c.preludeBuf
	// Keyed off the flush, not off whether an earlier rescue ran: a failed
	// subscription retry keeps the same dark model, so a cluster peer can still
	// serve — but only before the deferred error reaches the wire.
	// Recorded after the plan attempt and its same-model paid retry, so a 5xx on
	// either is what opens the plan-only sibling walk.
	if (servedOnSubscription(ctx) || c.subscriptionOnlySibling) && !preludeBuf.Committed() {
		c.credentials.RecordSubscriptionUnavailable(ctx, c.decision.Provider, c.decision.Model, c.proxyErr)
	}
	// A gateway overflow walks only the other gateways; same-gateway peers share the window that rejected it.
	overflowRescued := c.overflowRescueViable && isUpstreamContextOverflow(c.proxyErr)
	rescueOrder := c.siblingDecisions
	if overflowRescued {
		rescueOrder = c.overflowRescue
	}
	c.siblingRescueOwed = (c.siblingViable || overflowRescued) && !c.baselineAttempted && !c.deferredErrFlushed &&
		!(c.explicitRequestedModel && isSubscriptionPoolError(c.proxyErr)) &&
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
	c.winnerIdx, c.proxyErr = s.dispatchWithFallback(siblingCtx, dispatch.FailoverInputs{
		Writer:                 c.contentSink,
		Prelude:                c.preludeBuf,
		InitialDecision:        siblingDecision,
		Bindings:               siblingBindings,
		Attempt:                siblingAttempt,
		FlushErr:               c.flushErr,
		DeferFlushOnExhaustion: true,
		Purpose:                routeRes.dispatchPurpose(c.surface),
		Origin:                 routeRes.rescueOrigin(),
	})
	c.subscriptionPoolFailure = isSubscriptionPoolError(c.proxyErr)
	c.decision = siblingDecision
	c.bindings = siblingBindings
	c.marker = siblingMarker
	c.siblingFailoverUsed = c.proxyErr == nil
}

// servedProvider names the provider that served the last attempt, or fallback
// when no binding did.
func (c *rescueChain) servedProvider(fallback string) string {
	if c.subscriptionStateWinnerProvider != "" {
		return c.subscriptionStateWinnerProvider
	} else if c.winnerIdx >= 0 && c.winnerIdx < len(c.bindings) {
		return c.bindings[c.winnerIdx].Provider
	}
	return fallback
}
