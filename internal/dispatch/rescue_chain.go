package dispatch

import (
	"context"
	"log/slog"
	"net/http"

	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/translate"
)

type RescueChain struct {
	RescuePorts
	Markers          RescueMarkers
	HTTPRequest      *http.Request
	Log              *slog.Logger
	Env              *translate.RequestEnvelope
	RouteRes         RescueTurn
	Request          *router.Request
	Opts             translate.EmitOptions
	ContentSink      http.ResponseWriter
	PreludeBuf       *PreludeBuffer
	Attempt          UpstreamAttempt
	BuildAttempt     func(target router.Decision, opts translate.EmitOptions, marker string, fundingCtx context.Context) (UpstreamAttempt, error)
	FlushErr         func(w http.ResponseWriter, err error)
	Surface          inference.Purpose
	OverflowEstimate int
	OutputReserve    int
	RespSummary      *translate.ResponseSummary
	ReqStats         *providers.RequestMutationStats

	Decision     router.Decision
	Bindings     []catalog.ProviderBinding
	Marker       string
	ProxyErr     error
	EffortServed EffortResolution
	WinnerIdx    int

	PrimaryProvider                      string
	PrimaryModel                         string
	PrimaryDecision                      router.Decision
	PickedMarker                         string
	SubscriptionStateWinnerProvider      string
	SubscriptionPoolFailure              bool
	SubscriptionStatePaidTargetAttempted bool
	PrimaryFailureErr                    error
	PrimarySubscriptionArmFailure        error
	DeferredErrFlushed                   bool

	ExplicitRequestedModel  bool
	SiblingDecisions        []router.Decision
	SiblingViable           bool
	SubscriptionOnlySibling bool
	OverflowRescue          []router.Decision
	OverflowRescueViable    bool
	RescueArmed             bool
	OverflowRescueOnly      bool
	BaselineAttempted       bool
	CrossBindingRejected    bool

	SiblingFailoverUsed bool
	SiblingRescueRan    bool
	SiblingRescueOwed   bool
	RescuedPrimary      router.Decision
	RescuedPrimaryErr   error

	FinalProvider     string
	FailoverUsed      bool
	FailoverAttempted bool
}

func (c *RescueChain) ArmSiblings(ctx context.Context, rescueBlocked bool) context.Context {
	env := c.Env
	ctx = c.Funding.WithSiblingFunding(ctx, c.Decision)
	c.SiblingDecisions = c.Funding.PaidRescueDecisions(ctx, c.Routing.SiblingFailoverDecisions(ctx, c.Routing.RescueBasis(c.Decision, c.RouteRes), c.OverflowEstimate, env.SignatureTokenSavings(), c.OutputReserve))
	c.SiblingViable = len(c.SiblingDecisions) > 0 &&
		!rescueBlocked &&
		c.Routing.SiblingRescueAllowed(ctx, c.Routing.GatewaySiblingAllowed(ctx, c.SiblingDecisions[0]))
	c.SubscriptionOnlySibling = c.SiblingViable && !c.Routing.ShouldFailover(ctx) &&
		!c.Routing.GatewaySiblingAllowed(ctx, c.SiblingDecisions[0]) && c.Credentials.SubscriptionSiblingAllowed(ctx)
	c.OverflowRescue = c.Routing.OverflowRescueDecisions(ctx, c.Decision, c.OverflowEstimate, env.SignatureTokenSavings(), c.OutputReserve)
	c.OverflowRescueViable = len(c.OverflowRescue) > 0 &&
		!rescueBlocked &&
		c.Routing.SiblingRescueAllowed(ctx, true)
	c.RescueArmed = c.SiblingViable || c.OverflowRescueViable
	c.OverflowRescueOnly = c.OverflowRescueViable && !c.SiblingViable
	return ctx
}

func (c *RescueChain) HoldPrimary() {
	c.PrimaryProvider = c.Decision.Provider
	c.PrimaryModel = c.Decision.Model
	c.PrimaryDecision = c.Decision
	c.PickedMarker = c.Marker
}

func (c *RescueChain) DispatchPrimaryAttempt(ctx context.Context, deferFlush bool, onPaidTarget func(router.Decision)) {
	r := c.HTTPRequest
	routeRes := c.RouteRes
	c.WinnerIdx, c.ProxyErr = c.Dispatch(ctx, FailoverInputs{
		Writer:                          c.ContentSink,
		Prelude:                         c.PreludeBuf,
		InitialDecision:                 c.Decision,
		SubscriptionStateRequest:        c.TranslationEligibleRequest(),
		SubscriptionStateHeaders:        r.Header,
		SubscriptionStateWinnerProvider: &c.SubscriptionStateWinnerProvider,
		Alternatives: func() []router.Decision {
			if routeRes.HardPinned || routeRes.AuthoritativePerTurn {
				return nil
			}
			return c.Credentials.SubscriptionAlternativeDecisions(ctx, *c.Request, c.Decision)
		}(),
		BuildAlternative: func(target router.Decision) (UpstreamAttempt, error) {
			alternativeOpts, _ := c.Attempts.SwitchedOptions(ctx, c.Opts, target, routeRes.EscalateEffort)
			alternativeMarker := c.Markers.SubscriptionTarget(ctx, c.PrimaryDecision, target, c.PickedMarker)
			alternativeCtx := c.Credentials.ResolveCredentials(ctx, target.Provider, target.Model, r.Header)
			return c.BuildAttempt(target, alternativeOpts, alternativeMarker, alternativeCtx)
		},
		OnAlternative: func(target router.Decision) {
			overloadedModel := c.Decision.Model
			c.Decision = target
			c.Bindings = c.Routing.ResolveBindingsForDispatch(ctx, target)
			c.Marker = c.Markers.Sibling(ctx, overloadedModel, target.Model)
		},
		OnSubscriptionStateTarget: func(target router.Decision, targetBindings []catalog.ProviderBinding) {
			c.Marker = c.Markers.SubscriptionTarget(ctx, c.PrimaryDecision, target, c.PickedMarker)
			c.Decision = target
			c.Bindings = targetBindings
		},
		OnSubscriptionStatePaidTarget: func(target router.Decision, _ []catalog.ProviderBinding) {
			c.SubscriptionStatePaidTargetAttempted = true
			if onPaidTarget != nil {
				onPaidTarget(target)
			}
		},
		Bindings:               c.Bindings,
		Attempt:                c.Attempt,
		FlushErr:               c.FlushErr,
		DeferFlushOnExhaustion: deferFlush,
		Purpose:                routeRes.DispatchPurpose(c.Surface),
		Origin:                 routeRes.DispatchOrigin(c.Decision),
	})
	c.SubscriptionPoolFailure = c.Credentials.IsSubscriptionPoolError(c.ProxyErr)
}

func (c *RescueChain) NotePrimaryFailure(ctx context.Context) {
	c.PrimaryFailureErr = c.ProxyErr
	c.PrimarySubscriptionArmFailure = c.ProxyErr
	if (c.Credentials.ServedOnSubscription(ctx) || c.SubscriptionOnlySibling) && !c.PreludeBuf.Committed() {
		c.Credentials.RecordSubscriptionUnavailable(ctx, c.Decision.Provider, c.Decision.Model, c.ProxyErr)
	}
}

func (c *RescueChain) SiblingRescue(ctx context.Context) {
	preludeBuf := c.PreludeBuf
	if (c.Credentials.ServedOnSubscription(ctx) || c.SubscriptionOnlySibling) && !preludeBuf.Committed() {
		c.Credentials.RecordSubscriptionUnavailable(ctx, c.Decision.Provider, c.Decision.Model, c.ProxyErr)
	}
	overflowRescued := c.OverflowRescueViable && c.Overflow.IsContextOverflow(c.ProxyErr)
	rescueOrder := c.SiblingDecisions
	if overflowRescued {
		rescueOrder = c.OverflowRescue
	}
	c.SiblingRescueOwed = (c.SiblingViable || overflowRescued) && !c.BaselineAttempted && !c.DeferredErrFlushed &&
		(!c.ExplicitRequestedModel || !c.Credentials.IsSubscriptionPoolError(c.ProxyErr)) &&
		(!c.SubscriptionOnlySibling || c.Credentials.SubscriptionPoolUnavailable(ctx, c.Decision.Provider, c.Decision.Model))
	if c.SiblingRescueOwed && c.ProxyErr != nil && !preludeBuf.Committed() &&
		(providers.IsRetryable(c.ProxyErr) ||
			providers.IsUpstreamModelNotFound(c.ProxyErr) ||
			providers.IsUpstreamProviderBillingBlocked(c.ProxyErr) ||
			c.Credentials.IsSubscriptionPoolError(c.ProxyErr) ||
			c.CrossBindingRejected ||
			c.Overflow.RescuesCompactionOverflow(c.RouteRes.TurnType, c.ProxyErr) ||
			overflowRescued) {
		for _, siblingDecision := range rescueOrder {
			if c.SiblingRescueRan && (c.ProxyErr == nil || preludeBuf.Committed() || ctx.Err() != nil) {
				break
			}
			c.DispatchSibling(ctx, siblingDecision)
		}
	}
}

func (c *RescueChain) DispatchSibling(ctx context.Context, siblingDecision router.Decision) {
	r := c.HTTPRequest
	log := c.Log
	routeRes := c.RouteRes
	siblingOpts, siblingEffort := c.Attempts.SwitchedOptions(ctx, c.Opts, siblingDecision, routeRes.EscalateEffort)
	siblingCtx := c.Credentials.ResolveCredentials(c.Funding.PaidRescueContext(c.Credentials.RescueCredentialContext(ctx, siblingDecision.Provider), siblingDecision.Model), siblingDecision.Provider, siblingDecision.Model, r.Header)
	siblingOpts.FastMode = c.Attempts.FastMode(siblingCtx, siblingDecision.Model, siblingDecision.Provider)
	siblingBindings := c.Routing.ResolveBindingsForDispatch(siblingCtx, siblingDecision)
	siblingMarker := c.Markers.WithFailureDetail(ctx, c.Markers.Sibling(ctx, c.Decision.Model, siblingDecision.Model))
	siblingAttempt, siblingBuildErr := c.BuildAttempt(siblingDecision, siblingOpts, siblingMarker, siblingCtx)
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
			"err", c.ProxyErr)
		return
	}
	log.Warn("Sibling failover: model exhausted, retrying a same-cluster candidate",
		"failed_model", c.Decision.Model,
		"failed_provider", c.Decision.Provider,
		"primary_model", c.PrimaryModel,
		"sibling_model", siblingDecision.Model,
		"sibling_provider", siblingDecision.Provider,
		"upstream_status", UpstreamStatus(c.ProxyErr),
		"err", c.ProxyErr)
	c.Routing.RecordTurnFailure(ctx, c.Decision.Model)
	if !c.SiblingRescueRan {
		c.RescuedPrimary = c.Decision
		c.RescuedPrimaryErr = c.ProxyErr
	}
	c.SiblingRescueRan = true
	c.Routing.NoteRescueReadmission(ctx, c.Decision, siblingDecision)
	c.EffortServed = siblingEffort
	*c.RespSummary = translate.ResponseSummary{}
	if c.ReqStats != nil {
		*c.ReqStats = providers.RequestMutationStats{}
	}
	c.WinnerIdx, c.ProxyErr = c.Dispatch(siblingCtx, FailoverInputs{
		Writer:                 c.ContentSink,
		Prelude:                c.PreludeBuf,
		InitialDecision:        siblingDecision,
		Bindings:               siblingBindings,
		Attempt:                siblingAttempt,
		FlushErr:               c.FlushErr,
		DeferFlushOnExhaustion: true,
		Purpose:                routeRes.DispatchPurpose(c.Surface),
		Origin:                 routeRes.RescueOrigin(),
	})
	c.SubscriptionPoolFailure = c.Credentials.IsSubscriptionPoolError(c.ProxyErr)
	c.Decision = siblingDecision
	c.Bindings = siblingBindings
	c.Marker = siblingMarker
	c.SiblingFailoverUsed = c.ProxyErr == nil
}

func (c *RescueChain) ServedProvider(fallback string) string {
	if c.SubscriptionStateWinnerProvider != "" {
		return c.SubscriptionStateWinnerProvider
	} else if c.WinnerIdx >= 0 && c.WinnerIdx < len(c.Bindings) {
		return c.Bindings[c.WinnerIdx].Provider
	}
	return fallback
}

func (c *RescueChain) TranslationEligibleRequest() *router.Request {
	if c.RouteRes.TranslationEnabledProviders == nil {
		return c.Request
	}
	req := *c.Request
	req.EnabledProviders = c.RouteRes.TranslationEnabledProviders
	return &req
}
