package observability

import (
	"context"
	"net/http"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

func NewTracingTransport(base http.RoundTripper) http.RoundTripper {
	return otelhttp.NewTransport(base, otelhttp.WithPropagators(explicitHeadersWin{}))
}

func WrapHTTPClient(client *http.Client) *http.Client {
	if client == nil {
		client = &http.Client{}
	}
	wrapped := *client
	wrapped.Transport = NewTracingTransport(client.Transport)
	return &wrapped
}

type explicitHeadersWin struct{}

func (explicitHeadersWin) Inject(ctx context.Context, carrier propagation.TextMapCarrier) {
	injected := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, injected)
	for key, value := range injected {
		if carrier.Get(key) == "" {
			carrier.Set(key, value)
		}
	}
}

func (explicitHeadersWin) Extract(ctx context.Context, carrier propagation.TextMapCarrier) context.Context {
	return otel.GetTextMapPropagator().Extract(ctx, carrier)
}

func (explicitHeadersWin) Fields() []string {
	return otel.GetTextMapPropagator().Fields()
}
