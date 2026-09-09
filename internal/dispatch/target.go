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

// ErrTargetMismatch is returned when a prepared upstream request names a model
// other than the plan target, so the request never reaches the wire.
var ErrTargetMismatch = errors.New("prepared request does not match plan target")

// ValidatePreparedTarget checks that the wire-format body prepared for one
// attempt names the target the plan authorized. Bodies carry either the
// catalog ID (adapters rewrite to the upstream ID) or the upstream ID itself.
// Bodies without a top-level model (Gemini carries it in the URL path) are
// accepted; callers with a path-carried model use ValidateWireModel.
func ValidatePreparedTarget(prep providers.PreparedRequest, target inference.Target) error {
	model := gjson.GetBytes(prep.Body, "model")
	if !model.Exists() {
		return nil
	}
	return ValidateWireModel(model.String(), target)
}

// ValidateWireModel checks that the model identity about to be sent upstream
// is the plan target's catalog or upstream ID.
func ValidateWireModel(model string, target inference.Target) error {
	if model == "" {
		return fmt.Errorf("%w: empty wire model for target %s/%s", ErrTargetMismatch, target.Provider, target.CatalogID)
	}
	if model == target.CatalogID || (target.UpstreamID != "" && model == target.UpstreamID) {
		return nil
	}
	return fmt.Errorf("%w: wire model %q, plan target %s/%s (upstream %q)", ErrTargetMismatch, model, target.Provider, target.CatalogID, target.UpstreamID)
}

// GuardTarget wraps client so every Proxy call is checked against target
// before it reaches the wire. Surfaces that prepare the upstream body inside
// their attempt closure get the same pre-I/O guarantee as Buffered.
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
