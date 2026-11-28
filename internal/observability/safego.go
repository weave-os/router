package observability

import (
	"context"
	"log/slog"
	"time"
)

func SafeGo(log *slog.Logger, timeout time.Duration, name string, fn func(ctx context.Context)) {
	SafeGoContext(context.Background(), log, timeout, name, fn)
}

func SafeGoContext(parent context.Context, log *slog.Logger, timeout time.Duration, name string, fn func(ctx context.Context)) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Error("Background goroutine panicked", "goroutine", name, "panic", r)
			}
		}()
		ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), timeout)
		defer cancel()
		fn(ctx)
	}()
}

func StartLifecycleWorker(log *slog.Logger, name string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil && log != nil {
				log.Error("Background goroutine panicked", "goroutine", name, "panic", r)
			}
		}()
		fn()
	}()
}
