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

func NewUpstream(client providers.Client) Upstream {
	return Upstream{client: client}
}

func (u Upstream) Proxy(ctx context.Context, decision router.Decision, prep providers.PreparedRequest, w http.ResponseWriter, r *http.Request) error {
	return u.client.Proxy(ctx, decision, prep, w, r)
}
