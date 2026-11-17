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

	"weave-os/router/internal/auth"
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

type preludeBuffer = dispatch.PreludeBuffer

func newPreludeBuffer(w http.ResponseWriter) *preludeBuffer { return dispatch.NewPreludeBuffer(w) }

type dispatchAttempt func(ctx context.Context, decision router.Decision, p providers.Client) error

type failoverInputs struct {
	w                               http.ResponseWriter
	buf                             *dispatch.PreludeBuffer
	initialDecision                 router.Decision
	bindings                        []catalog.ProviderBinding
	attempt                         dispatchAttempt
	flushErr                        func(w http.ResponseWriter, err error)
	deferFlushOnExhaustion          bool
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

var errDispatchWithoutPurpose = errors.New("dispatchWithFallback: no inference purpose")

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
	if err := productEligibilityRefusal(ctx, entitlement.ModelBoundaryFromContext(ctx), in.initialDecision.Model); err != nil {
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

func committed(b *dispatch.PreludeBuffer) bool {
	if b == nil {
		return false
	}
	return b.Committed()
}

const subscriptionRotationBudget = 10 * time.Second

func (s *Service) WithClock(clock auth.Clock) *Service {
	s.clock = clock
	return s
}

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

func flushBufferedIfPresent(w http.ResponseWriter, err error) {
	flushUpstreamErrorRendered(w, err, nil)
}

func flushUpstreamErrorAsOpenAI(w http.ResponseWriter, err error) {
	flushUpstreamErrorRendered(w, err, translate.AnthropicErrorAsOpenAI)
}

func flushUpstreamErrorRendered(w http.ResponseWriter, err error, render func([]byte) []byte) {
	var resp *providers.UpstreamErrorResponse
	if !errors.As(err, &resp) || isContextOverflow(err) {
		return
	}
	body := resp.Body
	if render != nil {
		body = render(body)
	}
	body = attributedErrorBody(body, err)
	rewritten := !bytes.Equal(body, resp.Body)
	copyReplayedErrorHeaders(w.Header(), resp.Headers, rewritten)
	if rewritten {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(resp.Status)
	_, _ = w.Write(body)
}

func copyReplayedErrorHeaders(dst, upstream http.Header, bodyRewritten bool) {
	for k, vs := range upstream {
		canon := http.CanonicalHeaderKey(k)
		if _, hop := providers.HopByHopHeaders[canon]; hop || canon == "Content-Length" {
			continue
		}
		if bodyRewritten && (canon == "Content-Encoding" || canon == "Content-Type") {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

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
		body = attributedErrorBody(translate.AnthropicErrorAsOpenAI(resp.Body), err)
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

func anthropicErrorFrameBody(cls DispatchErrorClass) []byte {
	return []byte(fmt.Sprintf(`{"type":"error","error":{"type":%q,"message":%q}}`,
		errorFrameType(cls), cls.Message))
}

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

func flushUpstreamErrorAsAnthropic(w http.ResponseWriter, err error) {
	var resp *providers.UpstreamErrorResponse
	if !errors.As(err, &resp) || isContextOverflow(err) {
		return
	}
	body := attributedErrorBody(translate.OpenAIToAnthropicError(resp.Body), err)
	copyReplayedErrorHeaders(w.Header(), resp.Headers, !bytes.Equal(body, resp.Body))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.Status)
	_, _ = w.Write(body)
}

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
		attemptCtx := withSubscriptionAttempt(ctx)
		if target.Provider != selected.Provider {
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
