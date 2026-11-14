package proxy

import (
	"net/http"

	"weave-os/router/internal/sse"
	"weave-os/router/internal/telemetry"
)

// fallbackUsageResponseWriter keeps usage parsing functional when a service is
// constructed without an observability emitter, as in lightweight callers.
type fallbackUsageResponseWriter struct {
	inner     http.ResponseWriter
	extractor *telemetry.UsageExtractor
}

func newFallbackUsageExtractor(inner http.ResponseWriter, provider string) (*telemetry.UsageExtractor, http.ResponseWriter) {
	extractor := telemetry.NewUsageExtractor(provider)
	return extractor, &fallbackUsageResponseWriter{inner: inner, extractor: extractor}
}

func (w *fallbackUsageResponseWriter) Header() http.Header {
	return w.inner.Header()
}

func (w *fallbackUsageResponseWriter) WriteHeader(statusCode int) {
	sse.PrepareJSONResponse(w.inner)
	w.inner.WriteHeader(statusCode)
}

func (w *fallbackUsageResponseWriter) Write(p []byte) (int, error) {
	w.extractor.Consume(p)
	return sse.WriteJSONChunk(w.inner, p)
}

func (w *fallbackUsageResponseWriter) Flush() {
	if f, ok := w.inner.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *fallbackUsageResponseWriter) ArmOutputProgress(mark func()) bool {
	arm, ok := w.inner.(interface{ ArmOutputProgress(func()) bool })
	if !ok {
		return false
	}
	return arm.ArmOutputProgress(mark)
}
