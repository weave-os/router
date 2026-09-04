package proxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
)

// ForceClusterHeader constrains serving to one of the policy sidecar's
// classifier groups, leaving the within-group selection to the policy. It is
// the group-level analogue of ForceModelHeader, for headless callers that want
// a routing tier rather than one specific model.
//
// The value is deliberately not validated against a list baked into the router:
// the live group vocabulary belongs to the deployed policy artifact and changes
// with it, so the only authority is the roster the sidecar reports on this very
// request (see policy.ApplyClusterArmOverridesRequireMatch).
const ForceClusterHeader = "x-weave-force-cluster"

// ErrForcedClusterUnsupportedStrategy is returned when a caller forces a
// cluster on an installation whose strategy never runs a policy sidecar, so no
// roster exists to constrain serving to.
var ErrForcedClusterUnsupportedStrategy = errors.New("forced cluster requires an hmm strategy")

// ForcedClusterUnsupportedStrategyError carries the refused label and the
// strategy that was actually active, so the caller learns why the header can't
// be honored here.
type ForcedClusterUnsupportedStrategyError struct {
	Cluster  string
	Strategy string
}

// Error implements error.
func (e *ForcedClusterUnsupportedStrategyError) Error() string {
	return fmt.Sprintf("cannot force cluster %q: strategy %q runs no policy sidecar", e.Cluster, e.Strategy)
}

// Unwrap ties the typed error to ErrForcedClusterUnsupportedStrategy for errors.Is.
func (e *ForcedClusterUnsupportedStrategyError) Unwrap() error {
	return ErrForcedClusterUnsupportedStrategy
}

// applyForceClusterHeader resolves the x-weave-force-cluster header into the
// label carried on router.Request.ForceCluster. An absent header is a no-op.
//
// Only the strategy is checked here. Whether the named cluster exists and has
// an eligible model can't be known until the sidecar answers with this
// request's roster, so that check lives in the policy router; this is the one
// half that can be answered — and refused — before paying for a round trip.
func applyForceClusterHeader(ctx context.Context, r *http.Request) (string, error) {
	label := strings.ToLower(strings.TrimSpace(r.Header.Get(ForceClusterHeader)))
	if label == "" {
		return "", nil
	}
	strategy := router.StrategyFromContext(ctx)
	if !router.IsHMMStrategy(strategy) {
		observability.FromContext(ctx).Warn("x-weave-force-cluster: rejected on a strategy with no policy sidecar",
			"cluster", label,
			"strategy", strategy,
		)
		return "", &ForcedClusterUnsupportedStrategyError{Cluster: label, Strategy: string(strategy)}
	}
	observability.FromContext(ctx).Info("x-weave-force-cluster applied", "cluster", label, "strategy", strategy)
	return label, nil
}
