package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/tidwall/sjson"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/sse"
	"weave-os/router/internal/subscriptions/entitlement"
	"weave-os/router/internal/translate"
)

// preludeBuffer and newPreludeBuffer keep the entrypoints outside Inference
// Execution compiling against the buffer while they still construct it here.
type preludeBuffer = dispatch.PreludeBuffer

func newPreludeBuffer(w http.ResponseWriter) *preludeBuffer { return dispatch.NewPreludeBuffer(w) }

// dispatchAttempt does one per-binding dispatch. Returns the upstream error
// unmodified so dispatchWithFallback can decide whether to retry.
type dispatchAttempt func(ctx context.Context, decision router.Decision, p dispatch.Upstream) error

// failoverInputs bundles the inputs dispatchWithFallback needs that don't
// belong to a single attempt.
type failoverInputs struct {
	// w is the real client writer; flushErr writes here directly (bypassing
	// buf) so the client sees the upstream error in its expected format.
	w http.ResponseWriter
	// buf is the writer per-attempt code writes through; its Committed()
	// bit gates retry. nil when failover is impossible (single binding,
	// BYOK, legacy mode) — the loop then skips discard/commit entirely.
	buf *dispatch.PreludeBuffer
	// initialDecision carries the model + cluster metadata from the
	// router. dispatchWithFallback rewrites Provider per-attempt.
	initialDecision router.Decision
	// bindings is the ordered (provider, upstream-id, price) list, filtered
	// to providers wired in this deploy. Index 0 is primary, >0 fallbacks.
	bindings []catalog.ProviderBinding
	// attempt does one per-binding dispatch.
	attempt dispatchAttempt
	// flushErr renders the final-attempt upstream error to w in the
	// entry point's wire format. Optional — nil means do nothing on
	// exhaustion (the error is still returned to the caller).
	flushErr func(w http.ResponseWriter, err error)
	// deferFlushOnExhaustion suppresses flushErr on exhaustion (still
	// discards the buffer and returns the error), letting the caller run a
	// higher-level fallback instead (e.g. ProxyMessages' baseline failover
	// re-dispatching the Anthropic model when a routed OSS model exhausts).
	deferFlushOnExhaustion bool
	// purpose names the registered operation the walk is authorized under;
	// origin names the override source that fixed the decision's model.
	purpose                         inference.Purpose
	origin                          policy.OverrideSource
	subscriptionStateRequest        *router.Request
	subscriptionStateHeaders        http.Header
	alternatives                    []router.Decision
	buildAlternative                func(router.Decision) (dispatchAttempt, error)
	onAlternative                   func(router.Decision)
	onSubscriptionStateTarget       func(router.Decision, []catalog.ProviderBinding)
	onSubscriptionStatePaidTarget   func(router.Decision, []catalog.ProviderBinding)
	subscriptionStateWinnerProvider *string
}

// errDispatchWithoutPurpose rejects a walk no registered purpose authorizes:
// every dispatch must resolve a plan before bytes reach a provider.
var errDispatchWithoutPurpose = errors.New("dispatchWithFallback: no inference purpose")

// dispatchWithFallback authorizes the routed decision under in.purpose and
// runs the attempt closure against each binding in order through the
// dispatch executor, retrying on providers.IsRetryable errors while no bytes
// have reached the client. Returns the winning (or last-tried) index and
// error. On final-attempt error it flushes the upstream's own envelope to w
// instead of a generic 502.
func (s *Service) dispatchWithFallback(ctx context.Context, in failoverInputs) (winnerIdx int, err error) {
	ctx = withClaudeEnterpriseTurn(ctx, in.initialDecision)
	ctx = s.withSubscriptionFundingAuthorization(ctx, in.subscriptionStateHeaders)
	if in.subscriptionStateRequest != nil && subscriptionStateModelsEnabled(ctx) && in.buildAlternative != nil {
		return s.dispatchSubscriptionStateModels(ctx, in)
	}
	if subscriptionStateModelsEnabled(ctx) && !subscriptionAttemptOnly(ctx) {
		if _, allowed := modelSet(installationSubscriptionModelsWhenInactiveFromContext(ctx))[in.initialDecision.Model]; !allowed {
			return -1, router.ErrAllowlistEmptiesPool
		}
		if openRouterOverflowEnabled(ctx) && subscriptionAPIOnly(ctx) {
			var err error
			if ctx, in, err = s.withOpenRouterOverflowTarget(ctx, in); err != nil {
				return -1, err
			}
		}
	}
	if len(in.alternatives) > 0 && in.buildAlternative != nil {
		return s.dispatchSubscriptionAlternatives(ctx, in)
	}
	if len(in.purpose) == 0 {
		return -1, errDispatchWithoutPurpose
	}
	// Terminal gate on the product boundary. Candidate filtering already
	// dropped ineligible models, so this only catches a decision minted
	// outside the routed pool — a session pin, a hard-pinned auxiliary turn,
	// a /force-model, a fallback table. Refusing beats serving what the plan
	// does not sell.
	if err := catalog.CheckEligibility(entitlement.ModelBoundaryFromContext(ctx), in.initialDecision.Model); err != nil {
		return -1, err
	}
	if len(in.bindings) == 0 {
		if in.initialDecision.Reason == translate.ReasonUserForceModel {
			err := &providers.UpstreamErrorResponse{
				Status: http.StatusServiceUnavailable,
				Body: []byte(fmt.Sprintf(`{"error":{"message":%q,"type":"api_error"}}`,
					fmt.Sprintf("forced model %s unavailable: provider %s not configured", in.initialDecision.Model, in.initialDecision.Provider))),
			}
			if in.flushErr != nil && !in.deferFlushOnExhaustion {
				in.flushErr(in.w, err)
			}
			return -1, err
		}
		return -1, &providers.UpstreamStatusError{Status: http.StatusBadGateway}
	}
	plans, err := s.inferencePlans()
	if err != nil {
		return -1, err
	}
	plan, err := plans.ResolveRouted(policy.RoutedResolutionRequest{
		Purpose:  policy.Purpose(in.purpose),
		Decision: in.initialDecision,
		Bindings: in.bindings,
		Origin:   in.origin,
	})
	if err != nil {
		return -1, err
	}
	return s.dispatchPlanned(ctx, in, plan)
}

// committed is a nil-safe shorthand for in.buf.Committed(); the
// single-binding fast path passes buf=nil and is treated as not committed.
func committed(b *dispatch.PreludeBuffer) bool {
	if b == nil {
		return false
	}
	return b.Committed()
}

// subscriptionRotationBudget bounds how long included-capacity accounts and
// model alternatives may be leased and admitted, shared across one request's
// rotation walk. It never cancels an attempt already in flight: provider
// header/stream watchdogs and the caller own inference lifetime.
const subscriptionRotationBudget = 10 * time.Second

// clockNow reads the current time through the injectable clock, falling back
// to time.Now when no fake is wired.
func (s *Service) clockNow() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// shouldFailover reports failover eligibility. Customer-supplied
// credentials (BYOK or client key) bind to a single provider — retrying
// elsewhere would 401 unexpectedly, so we skip failover. A BYOK row for ANY
// provider disables it too: a failover binding re-resolves credentials and
// would prefer that key even if the primary binding used the deployment key.
func (s *Service) shouldFailover(ctx context.Context) bool {
	if s.credentials.BYOKOnly {
		return false
	}
	if CredentialsFromContext(ctx) != nil {
		return false
	}
	if len(externalKeysFromContext(ctx)) > 0 {
		return false
	}
	return true
}

// resolveBindingsForDispatch returns the ordered binding list to walk.
// When failover is disabled or unavailable, returns a single-element
// slice carrying the already-resolved decision provider.
func (s *Service) resolveBindingsForDispatch(ctx context.Context, decision router.Decision) []catalog.ProviderBinding {
	req := dispatch.BindingRequest{
		Decision:   decision,
		ImageCount: requestImageCountFromContext(ctx),
		UserForced: isUserForcedReason(decision.Reason),
		ForceModel: decision.Reason == translate.ReasonUserForceModel,
	}
	if s.shouldFailover(ctx) && s.credentials.KeyedProviders != nil {
		req.DeploymentKeyedProviders = s.credentials.KeyedProviders
		req.SubscriptionServes = managedSubscriptionCanServe(ctx, decision.Provider, decision.Model) && s.supportsSubscriptionTransport(decision.Provider)
		req.Excluded = s.excludedProvidersForRequest(ctx)
	}
	return dispatch.OrderedBindings(req)
}

// flushBufferedIfPresent writes an *UpstreamErrorResponse through to the
// client verbatim. No-op for other error types.
//
// Content-Length/Encoding are dropped: MaxBufferedErrorBytes caps the body
// we hold, so the upstream's advertised length may exceed what we actually
// write, breaking HTTP framing. net/http recomputes Content-Length itself.
func flushBufferedIfPresent(w http.ResponseWriter, err error) {
	var resp *providers.UpstreamErrorResponse
	// An overflow is left to the ingress handler, which answers in the
	// client's native prompt-too-long shape.
	if !errors.As(err, &resp) || isContextOverflow(err) {
		return
	}
	for k, vs := range resp.Headers {
		canon := http.CanonicalHeaderKey(k)
		if _, hop := providers.HopByHopHeaders[canon]; hop {
			continue
		}
		if canon == "Content-Length" || canon == "Content-Encoding" {
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	body := attributedErrorBody(resp.Body, err)
	if !bytes.Equal(body, resp.Body) {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(resp.Status)
	_, _ = w.Write(body)
}

// emitAnthropicSSEErrorEvent writes an Anthropic-shape `event: error` SSE
// frame and returns *UpstreamStatusError so flushErr becomes a no-op.
// Used when the upstream errors after Prelude already committed HTTP 200 +
// message_start — a JSON error envelope would corrupt the SSE stream, but
// an `event: error` frame terminates cleanly.
func emitAnthropicSSEErrorEvent(sink http.ResponseWriter, err error) error {
	var resp *providers.UpstreamErrorResponse
	status := http.StatusBadGateway
	body, _ := sjson.SetBytes([]byte(`{"type":"error","error":{"type":"api_error","message":""}}`), "error.message", attributedStreamFailure(err))
	switch cls, classified := ClassifyDispatchError(err); {
	case classified && cls.Kind == DispatchErrorContextWindowExceeded:
		status = cls.Status
		body = anthropicErrorFrameBody(cls)
	case errors.As(err, &resp):
		status = resp.Status
		body = attributedErrorBody(translate.OpenAIToAnthropicError(resp.Body), err)
	case classified:
		status = cls.Status
		body = anthropicErrorFrameBody(cls)
	}
	_, _ = sink.Write([]byte("event: error\ndata: "))
	_, _ = sse.WriteJSONChunk(sink, body)
	_, _ = sink.Write([]byte("\n\n"))
	if f, ok := sink.(http.Flusher); ok {
		f.Flush()
	}
	return &providers.UpstreamStatusError{Status: status, Cause: err}
}

// emitOpenAISSEErrorEvent is emitAnthropicSSEErrorEvent's OpenAI-shape
// counterpart: writes a verbatim `data: {...}` frame, used after
// OpenAIRoutingMarkerWriter has already committed HTTP 200.
func emitOpenAISSEErrorEvent(sink http.ResponseWriter, err error) error {
	var resp *providers.UpstreamErrorResponse
	status := http.StatusBadGateway
	body, _ := sjson.SetBytes([]byte(`{"error":{"message":"","type":"server_error","code":"upstream_error"}}`), "error.message", attributedStreamFailure(err))
	switch cls, classified := ClassifyDispatchError(err); {
	case classified && cls.Kind == DispatchErrorContextWindowExceeded:
		status = cls.Status
		body = openAIErrorFrameBody(cls)
	case errors.As(err, &resp):
		status = resp.Status
		body = attributedErrorBody(resp.Body, err)
	case classified:
		status = cls.Status
		body = openAIErrorFrameBody(cls)
	}
	_, _ = sink.Write([]byte("data: "))
	_, _ = sse.WriteJSONChunk(sink, body)
	_, _ = sink.Write([]byte("\n\n"))
	if f, ok := sink.(http.Flusher); ok {
		f.Flush()
	}
	return &providers.UpstreamStatusError{Status: status, Cause: err}
}

// anthropicErrorFrameBody renders a classified dispatch error as an
// Anthropic error body. The ingress handler cannot write its own envelope
// once the stream is committed, so the frame carries the class's own message
// rather than the generic upstream-failure text.
func anthropicErrorFrameBody(cls DispatchErrorClass) []byte {
	return []byte(fmt.Sprintf(`{"type":"error","error":{"type":%q,"message":%q}}`,
		errorFrameType(cls), cls.Message))
}

// openAIErrorFrameBody is anthropicErrorFrameBody's OpenAI-shape counterpart.
func openAIErrorFrameBody(cls DispatchErrorClass) []byte {
	code := OpenAIErrorCode(cls.Kind)
	if code == "" {
		code = "upstream_error"
	}
	return []byte(fmt.Sprintf(`{"error":{"message":%q,"type":%q,"code":%q}}`,
		cls.Message, errorFrameType(cls), code))
}

func errorFrameType(cls DispatchErrorClass) string {
	if cls.Kind.IsClientError() {
		return "invalid_request_error"
	}
	return "api_error"
}

// flushUpstreamErrorAsAnthropic is ProxyMessages' flushErr: translates the
// OpenAI-compat upstream's error body to Anthropic-shape JSON. No-op when
// err is not an *UpstreamErrorResponse.
func flushUpstreamErrorAsAnthropic(w http.ResponseWriter, err error) {
	var resp *providers.UpstreamErrorResponse
	if !errors.As(err, &resp) || isContextOverflow(err) {
		return
	}
	for k, vs := range resp.Headers {
		canon := http.CanonicalHeaderKey(k)
		if _, hop := providers.HopByHopHeaders[canon]; hop {
			continue
		}
		if canon == "Content-Type" || canon == "Content-Length" {
			continue // body is rewritten below
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.Status)
	_, _ = w.Write(attributedErrorBody(translate.OpenAIToAnthropicError(resp.Body), err))
}

// attemptIdxLabel formats i for the x-router-fallback-attempt header.
// Caller gates on i > 0.
func attemptIdxLabel(i int) string {
	return strconv.Itoa(i)
}

type subscriptionOnlyAttemptKey struct{}
type subscriptionAPIOnlyKey struct{}
type subscriptionRotationBudgetKey struct{}

func subscriptionAttemptOnly(ctx context.Context) bool {
	value, _ := ctx.Value(subscriptionOnlyAttemptKey{}).(bool)
	return value
}
func subscriptionAPIOnly(ctx context.Context) bool {
	value, _ := ctx.Value(subscriptionAPIOnlyKey{}).(bool)
	return value
}
func (s *Service) dispatchSubscriptionAlternatives(ctx context.Context, in failoverInputs) (int, error) {
	ctx, budget := withSubscriptionAdmissionBudget(ctx)
	selected := in.initialDecision
	if usage, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage); usage != nil {
		usage.IntendedModel = selected.Model
	}
	targets := append([]router.Decision{selected}, in.alternatives...)
	for index, target := range targets {
		if ctx.Err() != nil || budget.remaining <= 0 {
			break
		}
		attemptIn := in
		attemptIn.alternatives = nil
		attemptIn.deferFlushOnExhaustion = true
		if index > 0 {
			attempt, buildErr := in.buildAlternative(target)
			if buildErr != nil {
				continue
			}
			attemptIn.initialDecision = target
			attemptIn.attempt = attempt
			attemptIn.bindings = s.resolveBindingsForDispatch(ctx, target)
		}
		if in.buf != nil {
			in.buf.Discard()
		}
		attemptCtx := context.WithValue(ctx, subscriptionOnlyAttemptKey{}, true)
		if target.Provider != selected.Provider {
			// A selected provider's OAuth credential never carries to another provider.
			attemptCtx = clearCredentials(attemptCtx)
		}
		attemptCtx = s.resolveCredentials(attemptCtx, target.Provider, target.Model, http.Header{})
		winner, attemptErr := s.dispatchWithFallback(attemptCtx, attemptIn)
		if attemptErr == nil {
			if index > 0 && in.onAlternative != nil {
				in.onAlternative(target)
			}
			return winner, nil
		}
		if committed(in.buf) || ctx.Err() != nil {
			return winner, attemptErr
		}
	}
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	if paidFallbackForbidden(ctx) {
		return 0, subscriptionPoolExhausted(ctx)
	}
	if in.buf != nil {
		in.buf.Discard()
	}
	in.alternatives = nil
	apiCtx := s.resolveCredentials(withSuppressedClaudeSubscription(withSuppressedCodexSubscription(context.WithValue(ctx, subscriptionAPIOnlyKey{}, true))), selected.Provider, selected.Model, http.Header{})
	return s.dispatchWithFallback(apiCtx, in)
}
