package proxy

import (
	"context"
	"net/http"
	"slices"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/telemetry"
	"weave-os/router/internal/translate"
)

// messagesRescue is the Anthropic Messages rescue chain: early-stop redo,
// baseline failover and the subscription-credit retry ahead of the shared
// sibling rescue.
type messagesRescue struct {
	*rescueChain
	feats                   translate.RoutingFeatures
	agentShadowMode         bool
	earlyStop               *dispatch.EarlyStopGate
	streamCut               *streamCutObserver
	attemptBuildErr         error
	anthropicTierAttemptFor func(targetOpts translate.EmitOptions, prep providers.PreparedRequest, targetNotice translate.RouterNotice) *anthropicTierAttempt
	recordFastServed        func(fast bool)
	// Shared with the entrypoint's attempt closures, which write them too.
	extractor   **telemetry.UsageExtractor
	fastServed  *bool
	crossFormat *bool

	compactionHandoverOutcome handoverOutcome

	baselineModel             string
	baselineViable            bool
	baselineEligible          bool
	subscriptionRetryEligible bool
	earlyStopMode             flags.EarlyStopRedoMode
	earlyStopTarget           router.Decision
	earlyStopCandidates       []string
	requestedWindowPassed     bool

	earlyStopRedone               bool
	baselineFailoverUsed          bool
	capabilityRejected            bool
	deadArmRejected               bool
	subscriptionFailoverUsed      bool
	subscriptionRetryRan          bool
	subscriptionFailoverAttempted bool
}

func (m *messagesRescue) armBaseline(ctx context.Context) {
	env := m.env
	routeRes := m.routeRes
	feats := m.feats
	// In-turn baseline failover eligibility: when the router cost-routes to an
	// OSS/Gemini model and every binding fails, fall back to the requested
	// model on Anthropic instead of hard-failing. Eligible only when: not
	// BYOK/inbound-credential bound (those resolve to a single provider),
	// Anthropic isn't excluded for the installation (else failing over would
	// violate the exclusion contract), the routed model isn't already
	// Anthropic, the baseline is a distinct known Anthropic catalog model, and
	// the prompt fits its context window (a larger-window routed model can
	// carry a prompt the baseline would 400 as "prompt is too long").
	// Computed pre-dispatch so the primary dispatch defers its exhaustion flush.
	m.baselineModel = m.routing.BaselineFor(ctx, feats.Model)
	baselineCatalog, baselineKnown := catalog.ByID(m.baselineModel)
	_, m.explicitRequestedModel = catalog.ByID(m.req.RequestedModel)
	_, anthropicExcluded := m.routing.ExcludedProvidersForRequest(ctx)[providers.ProviderAnthropic]
	baselineAllowed := modelPermittedByAllowlist(ctx, m.baselineModel) &&
		modelInRequestSubset(ctx, m.baselineModel)
	// baselineViable omits authoritative-per-turn: that contract governs which
	// model the policy picks, not whether a provably-unservable request can be rescued.
	m.baselineViable = !m.agentShadowMode &&
		!routeRes.CallerModelPassthrough &&
		m.decision.Reason != translate.ReasonUserForceModel &&
		m.routing.ShouldFailover(ctx) &&
		!paidFallbackForbiddenForModel(ctx, m.baselineModel) &&
		!anthropicExcluded &&
		baselineAllowed &&
		m.decision.Provider != providers.ProviderAnthropic &&
		m.baselineModel != m.decision.Model &&
		baselineKnown && baselineCatalog.PrimaryProvider() == providers.ProviderAnthropic &&
		siblingFitsContext(nil, m.baselineModel, providers.ProviderAnthropic, m.overflowEstimate, env.SignatureTokenSavings(), m.outputReserve) &&
		!slices.Contains(observedOverflowModelsFromContext(ctx), m.baselineModel)
	m.baselineEligible = !routeRes.AuthoritativePerTurn && m.baselineViable
}

func (m *messagesRescue) armSubscriptionRetry(ctx context.Context) {
	// Subscription-credit failover eligibility. A Claude turn served on the
	// caller's subscription (sk-ant-oat) is pinned to a single Anthropic
	// binding, so a retryable 429/timeout has nowhere to fail over to and
	// reaches the client raw. This is the gap behind prod instability: the
	// observer-driven exhaustion suppression above only fires once a PRIOR
	// snapshot already read exhausted, but the binding 429 is usually the
	// first signal — the stale snapshot still reads "slack".
	//
	// When a non-subscription Anthropic key exists (BYOK or deployment), retry
	// the same model on it once: a retryable 429 on the subscription is served
	// on the Weave key (full cost) rather than surfaced raw — the same
	// fallback claudeSubscriptionExhausted takes pre-emptively, just driven by
	// the live error instead of a stale snapshot. Eligible only pre-commit, on
	// a subscription-served Anthropic turn, with a fallback key available.
	// Mutually exclusive with baselineEligible (non-Anthropic routed provider).
	// Suppressed when credits are depleted: this retry serves on the Weave/BYOK
	// key at full cost, which is exactly the paid spend that mode forbids — a
	// subscription throttle there surfaces raw instead. A linked-first turn's
	// credits are intact, so its throttle rolls over like any other.
	m.subscriptionRetryEligible = m.decision.Provider == providers.ProviderAnthropic &&
		!m.agentShadowMode &&
		servedOnSubscription(ctx) &&
		!paidFallbackForbiddenForModel(ctx, m.decision.Model) &&
		m.credentials.AnthropicFallbackKeyAvailable(ctx)
}

func (m *messagesRescue) armRescues(ctx context.Context) context.Context {
	return m.armSiblings(ctx, m.agentShadowMode || m.routeRes.CallerModelPassthrough || m.decision.Reason == translate.ReasonUserForceModel)
}

func (m *messagesRescue) armEarlyStop(ctx context.Context) {
	s := m.svc
	env := m.env
	routeRes := m.routeRes
	// A lower-tier model that ends a Claude Code tool-result turn without a
	// tool call is re-served on the turn's highest-tier candidate while the
	// client has seen nothing; shadow mode only logs that it would have been.
	m.earlyStopMode = s.earlyStopRedoMode(ctx, routeRes, m.decision, env.Stream(), m.agentShadowMode)
	if m.earlyStopMode != flags.EarlyStopRedoOff {
		basis := rescueBasisForTurn(m.decision, routeRes)
		target, found := m.routing.EarlyStopRedoTarget(ctx, basis, m.overflowEstimate, env.SignatureTokenSavings(), m.outputReserve)
		if found && m.routing.SiblingRescueAllowed(ctx, m.routing.GatewaySiblingAllowed(ctx, target)) && !paidFallbackForbiddenForModel(ctx, target.Model) {
			m.earlyStopTarget = target
			m.earlyStopCandidates = basis.Metadata.CandidateModels
			m.earlyStop.Observe(m.earlyStopMode == flags.EarlyStopRedoOn)
		} else {
			m.earlyStopMode = flags.EarlyStopRedoOff
		}
	}
}

func (m *messagesRescue) dispatchPrimary(ctx context.Context) {
	s := m.svc
	r := m.r
	log := m.log
	env := m.env
	routeRes := m.routeRes
	feats := m.feats
	// A compaction turn is how the client recovers from overflow; answering it
	// with prompt-too-long would fail the compaction itself.
	m.requestedWindowPassed = routeRes.TurnType != turntype.Compaction &&
		pastRequestedWindow(clientRequestedModel(ctx, feats.Model), m.decision.Model, m.overflowEstimate, m.outputReserve)
	if m.attemptBuildErr != nil {
		// Nothing was dispatched — enters the rescue chain as if every binding pre-committed failed.
		m.winnerIdx, m.proxyErr = -1, m.attemptBuildErr
	} else {
		m.dispatchPrimaryAttempt(ctx, m.baselineViable || m.subscriptionRetryEligible || m.rescueArmed, func(target router.Decision) {
			if !m.agentShadowMode && !routeRes.AuthoritativePerTurn && target.Provider != providers.ProviderAnthropic && !routeRes.HardPinned && !routeRes.Handover.Invoked && routeRes.PrefixTrimmed {
				log.Info("Context trimming detected on selected exhausted-state route; rewriting context with handover summary", "decision_model", target.Model)
				m.compactionHandoverOutcome = s.runCompactionHandover(ctx, env, r.Header, target.Model, *m.req)
			}
		})
	}
	if m.subscriptionStatePaidTargetAttempted {
		m.subscriptionRetryEligible = false
	}
}

// A released prelude can only take an SSE error frame, so every dispatch in
// the chain renders through this — a JSON envelope appended to a live stream
// is unparseable to the client.
func (m *messagesRescue) flushErrAsAnthropic(w http.ResponseWriter, err error) {
	env := m.env
	preludeBuf := m.preludeBuf
	err = asRequestedWindowOverflow(err, m.requestedWindowPassed, preludeBuf.Committed())
	if env.Stream() && preludeBuf.PreludeSent() {
		_ = emitAnthropicSSEErrorEvent(w, err)
		return
	}
	// Pre-commit, the handler renders the classified sentinel itself.
	if isSubscriptionPoolError(err) {
		return
	}
	flushUpstreamErrorAsAnthropic(w, err)
}

func (m *messagesRescue) redoIfStoppedEarly(ctx context.Context) {
	s := m.svc
	r := m.r
	log := m.log
	routeRes := m.routeRes
	opts := m.opts
	preludeBuf := m.preludeBuf
	if m.earlyStopMode != flags.EarlyStopRedoOff {
		stopped := m.proxyErr == nil && m.earlyStop.EarlyStop()
		if releaseCause := m.earlyStop.ReleaseReason(); !stopped && releaseCause != "" {
			log.Debug("Early-stop gate released the turn", "release_cause", releaseCause, "served_model", m.decision.Model)
		}
		if stopped {
			log.Info("Tool-result turn ended without a tool call below the top candidate tier",
				"served_model", m.decision.Model,
				"redo_model", m.earlyStopTarget.Model,
				"early_stop_redo_mode", string(m.earlyStopMode),
				"withheld", m.earlyStop.Withheld())
		}
		if stopped && m.earlyStop.Withheld() && !preludeBuf.Committed() {
			m.earlyStopRedone, m.proxyErr = s.redoEarlyStop(ctx, earlyStopRedo{
				Gate:    m.earlyStop,
				Prelude: preludeBuf,
				Header:  m.contentSink.Header(),
				Log:     log,
				Target:  m.earlyStopTarget,
				Build: func(target router.Decision) (dispatchAttempt, []catalog.ProviderBinding, string, effortResolution, context.Context, error) {
					redoOpts, redoEffort := switchedAttemptOptions(ctx, opts, target, routeRes.EscalateEffort, s.resolveEffort)
					redoCtx := m.credentials.ResolveCredentials(subscriptionStatePaidRescueContext(rescueCredentialContext(ctx, target.Provider), target.Model), target.Provider, target.Model, r.Header)
					redoOpts.FastMode = fastModeForAttempt(redoCtx, target.Model, target.Provider)
					redoMarker := suppressMarkerIfRequested(ctx, r.Header, modelSelectionMarkerForRequest(ctx, routeRes, siblingRoutingMarkerFor(routeRes, markerReasonEarlyStop, target.Model), target.Model, markerReasonEarlyStop))
					redoAttempt, buildErr := m.buildAttempt(target, redoOpts, redoMarker, redoCtx)
					return redoAttempt, m.routing.ResolveBindingsForDispatch(redoCtx, target), redoMarker, redoEffort, redoCtx, buildErr
				},
				Dispatch: func(redoCtx context.Context, target router.Decision, redoBindings []catalog.ProviderBinding, redoAttempt dispatchAttempt) (int, error) {
					return s.dispatchWithFallback(redoCtx, failoverInputs{
						Writer:                 m.contentSink,
						Prelude:                preludeBuf,
						InitialDecision:        target,
						Bindings:               redoBindings,
						Attempt:                redoAttempt,
						FlushErr:               m.flushErrAsAnthropic,
						DeferFlushOnExhaustion: true,
						Purpose:                routeRes.dispatchPurpose(inference.PurposeAnthropicMessages),
						Origin:                 routeRes.rescueOrigin(),
					})
				},
				Served: &earlyStopServed{
					Decision:   &m.decision,
					Bindings:   &m.bindings,
					Marker:     &m.marker,
					WinnerIdx:  &m.winnerIdx,
					Extractor:  m.extractor,
					Summary:    m.respSummary,
					ReqStats:   m.reqStats,
					FastServed: m.fastServed,
					Effort:     &m.effortServed,
				},
			})
			if m.earlyStopRedone {
				recordTurnFailure(ctx, m.primaryModel)
				for _, model := range lowerTierCandidates(m.earlyStopTarget.Model, m.earlyStopCandidates) {
					recordTurnFailure(ctx, model)
				}
			}
		}
		if releaseErr := m.earlyStop.Disarm(); releaseErr != nil && m.proxyErr == nil {
			m.proxyErr = releaseErr
		}
	}
}

// The deferred upstream error must reach the client exactly once: each
// rescue hands ownership to the next, and whichever declines to run flushes.
// Writing it forecloses any later rescue.
func (m *messagesRescue) flushDeferredErr() {
	env := m.env
	preludeBuf := m.preludeBuf
	if m.deferredErrFlushed {
		return
	}
	m.deferredErrFlushed = true
	m.proxyErr = asRequestedWindowOverflow(m.proxyErr, m.requestedWindowPassed, preludeBuf.Committed())
	if preludeBuf.Committed() {
		// Before synthesis, for the same reason as the native path.
		m.streamCut.noteCut(m.proxyErr)
	}
	if env.Stream() && preludeBuf.PreludeSent() {
		// The handler cannot render anything once the client has bytes,
		// so an in-stream frame is the only way the turn reports at all —
		// including for a pool sentinel, which would otherwise truncate
		// the stream silently. Keep the sentinel as the returned error so
		// the handler still classifies and logs it.
		sseErr := emitAnthropicSSEErrorEvent(m.contentSink, m.proxyErr)
		if !m.subscriptionPoolFailure {
			m.proxyErr = sseErr
		}
		return
	}
	// Pre-commit, the handler renders the classified sentinel itself.
	if m.subscriptionPoolFailure {
		return
	}
	flushUpstreamErrorAsAnthropic(m.contentSink, m.proxyErr)
}

func (m *messagesRescue) classifyPrimaryFailure() {
	log := m.log
	feats := m.feats
	// Capability rejection means the routed model cannot serve this shape at all —
	// rescue via baseline even when the policy owns per-turn selection.
	m.capabilityRejected = providers.IsUpstreamCapabilityRejection(m.proxyErr)
	// A cross-binding rescuable 400: another model's compiler accepts the
	// schemas, or another model ignores the Gemini thoughtSignature Gemini 3.x
	// rejected in the history.
	m.crossBindingRejected = providers.IsUpstreamSchemaRejection(m.proxyErr) || providers.IsUpstreamThoughtSignatureRejection(m.proxyErr)
	// A provably-dead-arm rejection (schema/capability/intrinsically-incompatible)
	// is snapshotted before any rescue runs — the rescue nils proxyErr on
	// success, which would otherwise hide the rejection from the post-rescue
	// pin-eviction decision below.
	m.deadArmRejected = m.capabilityRejected || m.crossBindingRejected || translate.IsIntrinsicallyIncompatible(m.proxyErr)
	if m.capabilityRejected {
		log.Error("Upstream rejected the request as unsupported by the routed model",
			"model", m.decision.Model,
			"provider", m.primaryProvider,
			"upstream_status", upstreamStatus(m.proxyErr),
			"has_images", feats.HasImages)
	}
}

func (m *messagesRescue) finish(ctx context.Context) {
	routeRes := m.routeRes
	preludeBuf := m.preludeBuf
	// No rescuer served (none ran, or the last one failed pre-commit); surface
	// the held error now so it's never dropped.
	m.proxyErr = asCompactionUnservable(routeRes.TurnType, m.decision, m.proxyErr)
	m.proxyErr = withClientTrimCounts(ctx, m.proxyErr, m.rescuedPrimaryErr)
	if (m.siblingRescueOwed || m.overflowRescueOnly) && m.proxyErr != nil && !preludeBuf.Committed() {
		m.flushDeferredErr()
	}
	m.proxyErr = asRequestedWindowOverflow(m.proxyErr, m.requestedWindowPassed, preludeBuf.Committed())

	m.subscriptionFailoverUsed = m.subscriptionFailoverUsed || subscriptionCredentialFallbackUsed(ctx)
	unservedProvider := m.primaryProvider
	if m.baselineAttempted {
		// Baseline ran but no binding served (winnerIdx == -1); the last
		// attempt was Anthropic with the baseline model, so finalProvider must
		// not revert to the OSS primary that never served it.
		unservedProvider = providers.ProviderAnthropic
	}
	m.finalProvider = m.servedProvider(unservedProvider)
	m.decision.Provider = m.finalProvider
	// Same-provider subscription->Weave retries keep finalProvider ==
	// primaryProvider, so OR in subscriptionFailoverUsed.
	m.failoverUsed = m.finalProvider != m.primaryProvider || m.subscriptionFailoverUsed || m.siblingFailoverUsed || m.earlyStopRedone
	m.failoverAttempted = m.failoverUsed || m.baselineAttempted || m.subscriptionFailoverAttempted || m.siblingRescueRan
}
