package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/policyregistry"
)

// probeScope decides whether an activated target is a dependency of the probe.
type probeScope string

const (
	// probeScopeStartup gates the container: a target with no activation yet must still boot,
	// otherwise its first activation can never be deployed.
	probeScopeStartup probeScope = "startup"
	// probeScopeAdmission gates admission: every dependency of a forwarded request.
	probeScopeAdmission probeScope = "admission"
)

// ReadinessHandler checks admission dependencies without minting an admission or calling inference.
// IAM token acquisition is checked here; deployment smoke must also verify the invoker grant.
func (h *Handler) ReadinessHandler(pingDatabase func(context.Context) error) http.Handler {
	return h.probeHandler(probeScopeAdmission, pingDatabase)
}

// StartupHandler checks the dependencies a gateway process needs to serve at all. It tolerates a
// target that has never been activated; requests still fail closed, because forwarding resolves
// the binding per request.
func (h *Handler) StartupHandler(pingDatabase func(context.Context) error) http.Handler {
	return h.probeHandler(probeScopeStartup, pingDatabase)
}

func (h *Handler) probeHandler(scope probeScope, pingDatabase func(context.Context) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if err := h.checkDependencies(ctx, scope, pingDatabase); err != nil {
			observability.FromContext(ctx).Warn("Gateway admission dependencies are not ready", "component", "router_gateway", "scope", string(scope), "err", err)
			http.Error(w, "Gateway is not ready.", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}

func (h *Handler) checkDependencies(ctx context.Context, scope probeScope, pingDatabase func(context.Context) error) error {
	if pingDatabase == nil || h.products == nil {
		return errors.New("gateway readiness requires database and environment configuration")
	}
	if err := pingDatabase(ctx); err != nil {
		return fmt.Errorf("ping admission database: %w", err)
	}
	if scope == probeScopeStartup {
		// Only an unwritten control state is boot-ready. Artifacts missing underneath an existing
		// activation are a broken target, and ErrNotFound cannot tell those apart further down.
		if _, err := h.registry.ReadServingState(ctx, h.defaultTarget()); errors.Is(err, policyregistry.ErrNotFound) {
			return nil
		}
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
