package observability

import (
	"net/http"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

func NewTracingTransport(base http.RoundTripper) http.RoundTripper {
	return otelhttp.NewTransport(base)
}

func WrapHTTPClient(client *http.Client) *http.Client {
	if client == nil {
		client = &http.Client{}
	}
	wrapped := *client
	wrapped.Transport = NewTracingTransport(client.Transport)
	return &wrapped
}
