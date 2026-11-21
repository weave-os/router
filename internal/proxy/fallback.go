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

var errDispatchWithoutPurpose = errors.New("dispatchWithFallback: no inference purpose")

func (s *Service) dispatchWithFallback(ctx context.Context, in dispatch.FailoverInputs) (winnerIdx int, err error) {
	ctx = withClaudeEnterpriseTurn(ctx, in.InitialDecision)
	ctx = s.withSubscriptionFundingAuthorization(ctx, in.SubscriptionStateHeaders)
	if in.SubscriptionStateRequest != nil && subscriptionStateModelsEnabled(ctx) && in.BuildAlternative != nil {
		return s.dispatchSubscriptionStateModels(ctx, in)
	}
	if subscriptionStateModelsEnabled(ctx) && !subscriptionAttemptOnly(ctx) {
		if _, allowed := modelSet(installationSubscriptionModelsWhenInactiveFromContext(ctx))[in.InitialDecision.Model]; !allowed {
			return -1, router.ErrAllowlistEmptiesPool
		}
		if openRouterOverflowEnabled(ctx) && subscriptionAPIOnly(ctx) {
			var err error
			if ctx, in, err = s.withOpenRouterOverflowTarget(ctx, in); err != nil {
				return -1, err
			}
		}
	}
	if len(in.Alternatives) > 0 && in.BuildAlternative != nil {
		return s.dispatchSubscriptionAlternatives(ctx, in)
	}
	if len(in.Purpose) == 0 {
		return -1, errDispatchWithoutPurpose
	}
	if err := productEligibilityRefusal(ctx, entitlement.ModelBoundaryFromContext(ctx), in.InitialDecision.Model); err != nil {
		return -1, err
	}
	if len(in.Bindings) == 0 {
		if in.InitialDecision.UserForcedModel() {
			err := &providers.UpstreamErrorResponse{
				Status: http.StatusServiceUnavailable,
				Body: []byte(fmt.Sprintf(`{"error":{"message":%q,"type":"api_error"}}`,
					fmt.Sprintf("forced model %s unavailable: provider %s not configured", in.InitialDecision.Model, in.InitialDecision.Provider))),
			}
			if in.FlushErr != nil && !in.DeferFlushOnExhaustion {
				in.FlushErr(in.Writer, err)
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
		Purpose:  policy.Purpose(in.Purpose),
		Decision: in.InitialDecision,
		Bindings: in.Bindings,
		Origin:   in.Origin,
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
		ForceModel: decision.UserForcedModel(),
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
func (s *Service) dispatchSubscriptionAlternatives(ctx context.Context, in dispatch.FailoverInputs) (int, error) {
	ctx, budget := withSubscriptionAdmissionBudget(ctx)
	selected := in.InitialDecision
	if usage, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage); usage != nil {
		usage.IntendedModel = selected.Model
	}
	targets := append([]router.Decision{selected}, in.Alternatives...)
	for index, target := range targets {
		if ctx.Err() != nil || budget.remaining <= 0 {
			break
		}
		attemptIn := in
		attemptIn.Alternatives = nil
		attemptIn.DeferFlushOnExhaustion = true
		if index > 0 {
			attempt, buildErr := in.BuildAlternative(target)
			if buildErr != nil {
				continue
			}
			attemptIn.InitialDecision = target
			attemptIn.Attempt = attempt
			attemptIn.Bindings = s.resolveBindingsForDispatch(ctx, target)
		}
		if in.Prelude != nil {
			in.Prelude.Discard()
		}
		attemptCtx := withSubscriptionAttempt(ctx)
		if target.Provider != selected.Provider {
			attemptCtx = clearCredentials(attemptCtx)
		}
		attemptCtx = s.resolveCredentials(attemptCtx, target.Provider, target.Model, http.Header{})
		winner, attemptErr := s.dispatchWithFallback(attemptCtx, attemptIn)
		if attemptErr == nil {
			if index > 0 && in.OnAlternative != nil {
				in.OnAlternative(target)
			}
			return winner, nil
		}
		if committed(in.Prelude) || ctx.Err() != nil {
			return winner, attemptErr
		}
	}
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	if paidFallbackForbidden(ctx) {
		return 0, subscriptionPoolExhausted(ctx)
	}
	if in.Prelude != nil {
		in.Prelude.Discard()
	}
	in.Alternatives = nil
	apiCtx := s.resolveCredentials(withSuppressedClaudeSubscription(withSuppressedCodexSubscription(context.WithValue(ctx, subscriptionAPIOnlyKey{}, true))), selected.Provider, selected.Model, http.Header{})
	return s.dispatchWithFallback(apiCtx, in)
}
