package dispatch

import (
	"context"
	"net/http"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
)

// ObservePrepared wraps client so observe sees each prepared body before it
// reaches the adapter.
func ObservePrepared(client providers.Client, observe func(ctx context.Context, body []byte)) providers.Client {
	return preparedObserver{Client: client, observe: observe}
}

type preparedObserver struct {
	providers.Client
	observe func(ctx context.Context, body []byte)
}

func (o preparedObserver) Proxy(ctx context.Context, decision router.Decision, prep providers.PreparedRequest, w http.ResponseWriter, r *http.Request) error {
	o.observe(ctx, prep.Body)
	return o.Client.Proxy(ctx, decision, prep, w, r)
}

func (o preparedObserver) Passthrough(ctx context.Context, prep providers.PreparedRequest, w http.ResponseWriter, r *http.Request) error {
	o.observe(ctx, prep.Body)
	return o.Client.Passthrough(ctx, prep, w, r)
}
