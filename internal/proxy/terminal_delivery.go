package proxy

import (
	"context"
	"errors"
	"net/http"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/translate"
)

type terminalDeliveryKey struct{}

// terminalDelivery owns the client outcome across attempts and nested protocol conversions.
type terminalDelivery struct {
	ctx       context.Context
	client    *deliveryResponseWriter
	format    translate.Format
	streaming bool
	delivery  *translate.NoticeDelivery
	prelude   *preludeBuffer
	buffers   []*responseCostBuffer
	responses *translate.ResponsesWriter
	after     func(error)
	finished  bool
}

type deliveryResponseWriter struct {
	http.ResponseWriter
	written bool
	status  int
}

func (w *deliveryResponseWriter) WriteHeader(status int) {
	if !w.written {
		w.status = status
	}
	w.written = true
	w.ResponseWriter.WriteHeader(status)
}
func (w *deliveryResponseWriter) Write(payload []byte) (int, error) {
	if !w.written {
		w.status = http.StatusOK
	}
	w.written = true
	return w.ResponseWriter.Write(payload)
}
func (w *deliveryResponseWriter) Flush() {
	w.written = true
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
func (w *deliveryResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func beginTerminalDelivery(ctx context.Context, w http.ResponseWriter, format translate.Format, streaming bool) (context.Context, http.ResponseWriter, func(*error)) {
	if terminalDeliveryFrom(ctx) != nil {
		return ctx, w, func(*error) {}
	}
	owner := &terminalDelivery{ctx: ctx, client: &deliveryResponseWriter{ResponseWriter: w}, format: format, streaming: streaming}
	ctx = context.WithValue(ctx, terminalDeliveryKey{}, owner)
	return ctx, owner.client, owner.finish
}

func terminalDeliveryFrom(ctx context.Context) *terminalDelivery {
	owner, _ := ctx.Value(terminalDeliveryKey{}).(*terminalDelivery)
	return owner
}

func registerResponseCostBuffer(ctx context.Context, buffer *responseCostBuffer) bool {
	owner := terminalDeliveryFrom(ctx)
	if owner == nil {
		return false
	}
	owner.buffers = append(owner.buffers, buffer)
	return true
}

func (o *terminalDelivery) finish(returnErr *error) {
	if o.finished {
		return
	}
	o.finished = true
	if o.ctx.Err() != nil && (*returnErr == nil || isStreamTerminalMissing(*returnErr)) {
		*returnErr = o.ctx.Err()
	}
	if o.ctx.Err() == nil {
		if o.delivery != nil {
			if err := o.delivery.Finalize(); err != nil && *returnErr == nil {
				*returnErr = err
			}
		}
		if *returnErr != nil {
			o.deliver(*returnErr)
		}
	}
	for index := len(o.buffers) - 1; index >= 0; index-- {
		if *returnErr != nil {
			continue
		}
		if err := o.buffers[index].FlushToClient(); err != nil {
			*returnErr = err
		}
	}
	if o.after != nil {
		o.after(*returnErr)
	}
}

func (o *terminalDelivery) deliver(cause error) {
	// A native provider may already have delivered an HTTP error. Its complete
	// envelope is the client outcome, even for a requested stream.
	if o.client.written && o.client.status >= http.StatusBadRequest {
		return
	}
	class, classified := ClassifyDispatchError(cause)
	if !classified {
		class = DispatchErrorClass{Status: http.StatusBadGateway, Message: "Upstream call failed.", LogLevel: "error", LogMessage: "Proxy failed"}
	}
	failure := translate.TerminalFailure{Status: class.Status, Message: class.Message, Type: errorFrameType(class), Code: OpenAIErrorCode(class.Kind), GeminiStatus: geminiFailureStatus(class.Kind, class.Status)}
	if failure.Code == "" {
		failure.Code = "upstream_error"
	}
	if errors.Is(cause, ErrGeminiCrossFormatUnsupported) {
		failure.Status = http.StatusNotImplemented
		failure.GeminiStatus = "UNIMPLEMENTED"
		failure.Message = "Cross-format request not supported by the upstream Gemini provider."
	}
	if class.RetryAfter {
		o.client.Header().Set("Retry-After", "1")
	}
	LogDispatchErrorClass(observability.FromContext(o.ctx), class, cause)
	var upstream *providers.UpstreamErrorResponse
	var flushed *providers.UpstreamStatusError
	if class.Kind == DispatchErrorUpstreamStatus {
		switch {
		case errors.As(cause, &upstream):
			failure.ProviderBody = upstream.Body
			if !o.client.written {
				copyFailureHeaders(o.client.Header(), upstream.Headers)
			}
		case errors.As(cause, &flushed):
			failure.ProviderBody = flushed.Body
			if !o.client.written {
				copyFailureHeaders(o.client.Header(), flushed.Headers)
			}
		}
	}
	streaming := o.streaming && o.client.written
	// Responses clients use this terminal code to initiate compaction even before a prelude.
	if o.format == translate.FormatOpenAIResponses && o.streaming && class.Kind == DispatchErrorContextWindowExceeded {
		streaming = true
	}
	delivery := o.delivery
	if !streaming || delivery == nil {
		caps := translate.NoticeCapabilities{Format: o.format, Streaming: streaming}
		delivery = translate.NewNoticeDelivery(o.ctx, translate.NewProtocolNoticeSink(o.client, caps), caps)
	} else if o.prelude != nil && !o.prelude.Committed() {
		o.prelude.Discard()
		o.prelude.Seal()
	}
	if o.responses != nil {
		failure.ResponsesSnapshot = o.responses.FailureSnapshot()
	}
	if err := delivery.DeliverFailure(failure, streaming); err != nil {
		observability.FromContext(o.ctx).Error("Failed to deliver terminal failure", "err", err)
	}
}

func copyFailureHeaders(target, source http.Header) {
	for name, values := range source {
		canonical := http.CanonicalHeaderKey(name)
		if _, hop := providers.HopByHopHeaders[canonical]; hop {
			continue
		}
		switch canonical {
		case "Content-Type", "Content-Length", "Content-Encoding":
			continue
		}
		target[canonical] = append([]string(nil), values...)
	}
}

func geminiFailureStatus(kind DispatchErrorKind, status int) string {
	switch kind {
	case DispatchErrorNotImplemented:
		return "UNIMPLEMENTED"
	case DispatchErrorProviderNotConfigured, DispatchErrorNoEligibleProvider:
		return "FAILED_PRECONDITION"
	case DispatchErrorRequestNotJSONObject, DispatchErrorContextWindowExceeded, DispatchErrorInvalidRoutingKnobs, DispatchErrorPlanOverrideRejected:
		return "INVALID_ARGUMENT"
	case DispatchErrorRLPolicyUnavailable, DispatchErrorBanditUnavailable, DispatchErrorHMMUnavailable, DispatchErrorPolicyUnavailable, DispatchErrorClusterUnavailable, DispatchErrorPlanUnresolvable, DispatchErrorPolicyPinUnavailable:
		return "UNAVAILABLE"
	default:
		switch status {
		case http.StatusTooManyRequests:
			return "RESOURCE_EXHAUSTED"
		case http.StatusUnauthorized:
			return "UNAUTHENTICATED"
		case http.StatusForbidden:
			return "PERMISSION_DENIED"
		case http.StatusBadGateway, http.StatusServiceUnavailable:
			return "UNAVAILABLE"
		default:
			return "UPSTREAM_ERROR"
		}
	}
}
