package observability

import (
	"net/http"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// NewTracingTransport wraps base with OpenTelemetry HTTP client tracing and
// W3C trace-context propagation. A nil base uses http.DefaultTransport.
func NewTracingTransport(base http.RoundTripper) http.RoundTripper {
	return otelhttp.NewTransport(base)
}

// WrapHTTPClient returns a copy of client whose transport emits OpenTelemetry
// client spans and propagates W3C trace context. The original client and its
// redirect, cookie, and timeout settings are unchanged.
func WrapHTTPClient(client *http.Client) *http.Client {
	if client == nil {
		client = &http.Client{}
	}
	wrapped := *client
	wrapped.Transport = NewTracingTransport(client.Transport)
	return &wrapped
}
