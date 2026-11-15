package dispatch

import (
	"context"
	"net/http"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
)

// Upstream is the provider an attempt sends its prepared request through. The
// executor hands one to each attempt, so code outside dispatch sends through
// the executor's choice instead of holding a provider client.
type Upstream struct {
	client providers.Client
}

// NewUpstream wraps the client chosen for one attempt. Callers pass the client
// already guarded for the planned target, so Upstream adds no check of its own.
func NewUpstream(client providers.Client) Upstream {
	return Upstream{client: client}
}

// Proxy forwards to the wrapped client unchanged; it exists so the provider
// call is made from internal/dispatch rather than by the attempt closure.
func (u Upstream) Proxy(ctx context.Context, decision router.Decision, prep providers.PreparedRequest, w http.ResponseWriter, r *http.Request) error {
	return u.client.Proxy(ctx, decision, prep, w, r)
}
