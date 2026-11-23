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
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/ingress"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/sse"
	"weave-os/router/internal/translate"
)

type preludeBuffer = dispatch.PreludeBuffer

func newPreludeBuffer(w http.ResponseWriter) *preludeBuffer { return dispatch.NewPreludeBuffer(w) }

func (s *Service) dispatchWithFallback(ctx context.Context, in dispatch.FailoverInputs) (winnerIdx int, err error) {
	return fallbackFor(s).Dispatch(ctx, in)
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
	if len(credentials.ExternalKeysFromContext(ctx)) > 0 {
		return false
	}
	return true
}

func (s *Service) resolveBindingsForDispatch(ctx context.Context, decision router.Decision) []catalog.ProviderBinding {
	req := dispatch.BindingRequest{
		Decision:   decision,
		ImageCount: requestcontext.RequestImageCountFromContext(ctx),
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

type sseErrorFrame struct {
	fallbackBody      string
	prefix            string
	translateUpstream func([]byte) []byte
	classifiedBody    func(ingress.DispatchErrorClass) []byte
}

var (
	anthropicSSEErrorFrame = sseErrorFrame{
		fallbackBody:      `{"type":"error","error":{"type":"api_error","message":""}}`,
		prefix:            "event: error\ndata: ",
		translateUpstream: translate.OpenAIToAnthropicError,
		classifiedBody:    anthropicErrorFrameBody,
	}
	openAISSEErrorFrame = sseErrorFrame{
		fallbackBody:      `{"error":{"message":"","type":"server_error","code":"upstream_error"}}`,
		prefix:            "data: ",
		translateUpstream: translate.AnthropicErrorAsOpenAI,
		classifiedBody:    openAIErrorFrameBody,
	}
)

func emitAnthropicSSEErrorEvent(sink http.ResponseWriter, err error) error {
	return emitSSEErrorEvent(sink, err, anthropicSSEErrorFrame)
}

func emitOpenAISSEErrorEvent(sink http.ResponseWriter, err error) error {
	return emitSSEErrorEvent(sink, err, openAISSEErrorFrame)
}

func emitSSEErrorEvent(sink http.ResponseWriter, err error, frame sseErrorFrame) error {
	var resp *providers.UpstreamErrorResponse
	status := http.StatusBadGateway
	body, _ := sjson.SetBytes([]byte(frame.fallbackBody), "error.message", attributedStreamFailure(err))
	switch cls, classified := ClassifyDispatchError(err); {
	case classified && cls.Kind == ingress.DispatchErrorContextWindowExceeded:
		status = cls.Status
		body = frame.classifiedBody(cls)
	case errors.As(err, &resp):
		status = resp.Status
		body = attributedErrorBody(frame.translateUpstream(resp.Body), err)
	case classified:
		status = cls.Status
		body = frame.classifiedBody(cls)
	}
	_, _ = sink.Write([]byte(frame.prefix))
	_, _ = sse.WriteJSONChunk(sink, body)
	_, _ = sink.Write([]byte("\n\n"))
	if f, ok := sink.(http.Flusher); ok {
		f.Flush()
	}
	return &providers.UpstreamStatusError{Status: status, Cause: err}
}

func anthropicErrorFrameBody(cls ingress.DispatchErrorClass) []byte {
	return []byte(fmt.Sprintf(`{"type":"error","error":{"type":%q,"message":%q}}`,
		errorFrameType(cls), cls.Message))
}

func openAIErrorFrameBody(cls ingress.DispatchErrorClass) []byte {
	code := OpenAIErrorCode(cls.Kind)
	if code == "" {
		code = "upstream_error"
	}
	return []byte(fmt.Sprintf(`{"error":{"message":%q,"type":%q,"code":%q}}`,
		cls.Message, errorFrameType(cls), code))
}

func errorFrameType(cls ingress.DispatchErrorClass) string {
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
