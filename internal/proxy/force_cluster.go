package proxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/continuity"
)

const ForceClusterHeader = "x-weave-force-cluster"

var ErrForcedClusterUnsupportedStrategy = errors.New("forced cluster requires an hmm strategy")

type ForcedClusterUnsupportedStrategyError struct {
	Cluster  string
	Strategy string
}

func (e *ForcedClusterUnsupportedStrategyError) Error() string {
	return fmt.Sprintf("cannot force cluster %q: strategy %q runs no policy sidecar", e.Cluster, e.Strategy)
}

func (e *ForcedClusterUnsupportedStrategyError) Unwrap() error {
	return ErrForcedClusterUnsupportedStrategy
}

func applyForceClusterHeader(ports continuity.StrategyPorts, ctx context.Context, strategy router.Strategy, r *http.Request) (string, error) {
	if planHasDedicatedServingPolicy(ctx) {
		return "", nil
	}
	label := strings.ToLower(strings.TrimSpace(r.Header.Get(ForceClusterHeader)))
	if label == "" {
		return "", nil
	}
	if !ports.Ownership.Owns(strategy) {
		observability.FromContext(ctx).Warn("x-weave-force-cluster: rejected on a strategy with no policy sidecar",
			"cluster", label,
			"strategy", strategy,
		)
		return "", &ForcedClusterUnsupportedStrategyError{Cluster: label, Strategy: string(strategy)}
	}
	observability.FromContext(ctx).Info("x-weave-force-cluster applied", "cluster", label, "strategy", strategy)
	return label, nil
}
