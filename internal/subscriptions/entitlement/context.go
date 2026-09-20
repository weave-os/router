package entitlement

import (
	"context"
	"fmt"
	"sync/atomic"
)

type coverageContextKey struct{}

// coverageBinding carries the admitted coverage plus the per-request action
// counter. One request can serve several upstream actions (handover summary,
// proactive compaction, sibling failover), and each needs its own durable
// action identifier while staying stable under a duplicated settlement.
type coverageBinding struct {
	coverage Coverage
	actions  atomic.Int64
}

// WithCoverage stamps the admitted coverage onto the request context so the
// settlement path books against the windows admission observed.
func WithCoverage(ctx context.Context, coverage Coverage) context.Context {
	binding := &coverageBinding{coverage: coverage}
	return context.WithValue(ctx, coverageContextKey{}, binding)
}

// CoverageFromContext returns the admitted coverage, if the request was
// admitted against an individual subscription allowance.
func CoverageFromContext(ctx context.Context) (Coverage, bool) {
	binding, ok := ctx.Value(coverageContextKey{}).(*coverageBinding)
	if !ok {
		return Coverage{}, false
	}
	return binding.coverage, true
}

// NextActionID mints the next action identifier for a router request. Returns
// false when the request carries no coverage.
func NextActionID(ctx context.Context, routerRequestID string) (string, bool) {
	binding, ok := ctx.Value(coverageContextKey{}).(*coverageBinding)
	if !ok || routerRequestID == "" {
		return "", false
	}
	return fmt.Sprintf("%s:%d", routerRequestID, binding.actions.Add(1)), true
}
