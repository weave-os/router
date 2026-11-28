package dispatch

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"

	"github.com/tidwall/gjson"
)

var ErrTargetMismatch = errors.New("prepared request does not match plan target")

var ErrRoutingModelOnWire = errors.New("routing model must not be sent to an upstream provider")

func ValidatePreparedTarget(prep providers.PreparedRequest, target inference.Target) error {
	model := gjson.GetBytes(prep.Body, "model")
	if !model.Exists() {
		return nil
	}
	return ValidateWireModel(model.String(), target)
}

func ValidateWireModel(model string, target inference.Target) error {
	if model == "" {
		return fmt.Errorf("%w: empty wire model for target %s/%s", ErrTargetMismatch, target.Provider, target.CatalogID)
	}
	if router.IsRoutingModel(model) {
		return fmt.Errorf("%w: %w: wire model %q for target %s/%s", ErrTargetMismatch, ErrRoutingModelOnWire, model, target.Provider, target.CatalogID)
	}
	if model == target.CatalogID || (target.UpstreamID != "" && model == target.UpstreamID) {
		return nil
	}
	return fmt.Errorf("%w: wire model %q, plan target %s/%s (upstream %q)", ErrTargetMismatch, model, target.Provider, target.CatalogID, target.UpstreamID)
}

func GuardTarget(client providers.Client, target inference.Target) providers.Client {
	return targetGuard{Client: client, target: target}
}

type targetGuard struct {
	providers.Client
	target inference.Target
}

func (g targetGuard) Proxy(ctx context.Context, decision router.Decision, prep providers.PreparedRequest, w http.ResponseWriter, r *http.Request) error {
	if err := ValidatePreparedTarget(prep, g.target); err != nil {
		return err
	}
	if decision.Model != g.target.CatalogID || decision.Provider != g.target.Provider {
		return fmt.Errorf("%w: decision %s/%s, plan target %s/%s", ErrTargetMismatch, decision.Provider, decision.Model, g.target.Provider, g.target.CatalogID)
	}
	return g.Client.Proxy(ctx, decision, prep, w, r)
}
