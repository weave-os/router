package entitlement

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"
)

type coverageContextKey struct{}

type coverageBinding struct {
	coverage Coverage
	actions  atomic.Int64
	failed   atomic.Bool
}

func WithCoverage(ctx context.Context, coverage Coverage) context.Context {
	binding := &coverageBinding{coverage: coverage}
	return context.WithValue(ctx, coverageContextKey{}, binding)
}

func CoverageFromContext(ctx context.Context) (Coverage, bool) {
	binding, ok := ctx.Value(coverageContextKey{}).(*coverageBinding)
	if !ok {
		return Coverage{}, false
	}
	return binding.coverage, true
}

func NextActionID(ctx context.Context, routerRequestID string) (string, bool) {
	binding, ok := ctx.Value(coverageContextKey{}).(*coverageBinding)
	if !ok || routerRequestID == "" {
		return "", false
	}
	return fmt.Sprintf("%s:%d", routerRequestID, binding.actions.Add(1)), true
}

func MarkSettlementFailed(ctx context.Context) {
	if binding, ok := ctx.Value(coverageContextKey{}).(*coverageBinding); ok {
		binding.failed.Store(true)
	}
}

func SettlementFailed(ctx context.Context) bool {
	binding, ok := ctx.Value(coverageContextKey{}).(*coverageBinding)
	return ok && binding.failed.Load()
}

type spentWindowContextKey struct{}

type SpentWindow struct {
	Plan     Plan
	Kind     PeriodKind
	ResetsAt time.Time
}

func WithSpentWindow(ctx context.Context, window SpentWindow) context.Context {
	return context.WithValue(ctx, spentWindowContextKey{}, window)
}

func SpentWindowFromContext(ctx context.Context) (SpentWindow, bool) {
	window, ok := ctx.Value(spentWindowContextKey{}).(SpentWindow)
	return window, ok
}
