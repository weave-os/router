package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/policyregistry"
)

// ReadinessHandler retains dependency diagnostics for release and operator checks.
func (h *Handler) ReadinessHandler(pingDatabase func(context.Context) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if err := h.checkDependencies(ctx, pingDatabase); err != nil {
			observability.FromContext(ctx).Warn("Gateway admission dependencies are not ready", "component", "router_gateway", "err", err)
			http.Error(w, "Gateway is not ready.", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}

// Warmup requires an activated binding and exercises IAM and the retained worker
// transport without admitting a session or invoking inference.
func (h *Handler) Warmup(ctx context.Context, warmDatabase func(context.Context) error) error {
	if err := warmDatabase(ctx); err != nil {
		return err
	}
	binding, err := h.defaultBinding(ctx)
	if err != nil {
		return fmt.Errorf("resolve startup serving binding: %w", err)
	}
	token, err := h.authorizer.IdentityToken(ctx, binding.Router.Audience)
	if err != nil {
		return fmt.Errorf("initialize worker IAM: %w", err)
	}
	destination, err := url.Parse(binding.Router.URL)
	if err != nil {
		return fmt.Errorf("parse startup worker destination: %w", err)
	}
	destination.Path = "/readyz"
	destination.RawPath = ""
	destination.RawQuery = ""
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, destination.String(), nil)
	if err != nil {
		return err
	}
	request.Header.Set(policyregistry.ServerlessAuthorizationHeader, "Bearer "+token)
	response, err := h.transport.RoundTrip(request)
	if err != nil {
		return fmt.Errorf("exercise worker transport: %w", err)
	}
	defer response.Body.Close()
	// Bounded consumption permits reuse for the small diagnostic response.
	if _, err := io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10)); err != nil {
		return fmt.Errorf("read worker startup response: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("worker startup returned status %d", response.StatusCode)
	}
	return nil
}

func (h *Handler) checkDependencies(ctx context.Context, pingDatabase func(context.Context) error) error {
	if pingDatabase == nil || h.products == nil {
		return errors.New("gateway readiness requires database and environment configuration")
	}
	if err := pingDatabase(ctx); err != nil {
		return fmt.Errorf("ping admission database: %w", err)
	}
	binding, err := h.defaultBinding(ctx)
	if err != nil {
		return fmt.Errorf("read default serving binding: %w", err)
	}
	if _, err := h.authorizer.IdentityToken(ctx, binding.Router.Audience); err != nil {
		return fmt.Errorf("acquire worker IAM token: %w", err)
	}
	return nil
}
