package requestcontext

import (
	"context"
	"slices"
	"sync"
)

type turnFailures struct {
	mu     sync.Mutex
	models []string
}

type turnFailuresContextKey struct{}

func WithTurnFailures(ctx context.Context) context.Context {
	if turnFailuresFromContext(ctx) != nil {
		return ctx
	}
	return context.WithValue(ctx, turnFailuresContextKey{}, &turnFailures{})
}

func turnFailuresFromContext(ctx context.Context) *turnFailures {
	failures, _ := ctx.Value(turnFailuresContextKey{}).(*turnFailures)
	return failures
}

func RecordTurnFailure(ctx context.Context, model string) {
	failures := turnFailuresFromContext(ctx)
	if failures == nil || model == "" {
		return
	}
	failures.mu.Lock()
	defer failures.mu.Unlock()
	if !slices.Contains(failures.models, model) {
		failures.models = append(failures.models, model)
	}
}

func TurnFailedModels(ctx context.Context) []string {
	failures := turnFailuresFromContext(ctx)
	failures.mu.Lock()
	models := slices.Clone(failures.models)
	failures.mu.Unlock()
	return models
}
