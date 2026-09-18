package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"weave-os/router/internal/observability"
)

// ReadinessHandler checks admission dependencies without minting an admission or calling inference.
// IAM token acquisition is checked here; deployment smoke must also verify the invoker grant.
func (h *Handler) ReadinessHandler(pingDatabase func(context.Context) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if err := h.checkReadiness(ctx, pingDatabase); err != nil {
			observability.FromContext(ctx).Warn("Gateway admission dependencies are not ready", "component", "router_gateway", "err", err)
			http.Error(w, "Gateway is not ready.", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}

func (h *Handler) checkReadiness(ctx context.Context, pingDatabase func(context.Context) error) error {
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
