package proxy

import (
	"context"
	"net/http"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
)

type captureTestRouter struct {
	calls    int
	requests []router.Request
	decision router.Decision
}

func (r *captureTestRouter) Route(_ context.Context, req router.Request) (router.Decision, error) {
	r.calls++
	r.requests = append(r.requests, req)
	return r.decision, nil
}

type captureTestProvider struct {
	body []byte
}

func (*captureTestProvider) SupportsSubscriptions() bool { return true }

func (p *captureTestProvider) Proxy(
	_ context.Context,
	_ router.Decision,
	req providers.PreparedRequest,
	_ http.ResponseWriter,
	_ *http.Request,
) error {
	p.body = append([]byte(nil), req.Body...)
	return nil
}

func (p *captureTestProvider) Passthrough(
	_ context.Context,
	req providers.PreparedRequest,
	_ http.ResponseWriter,
	_ *http.Request,
) error {
	p.body = append([]byte(nil), req.Body...)
	return nil
}
