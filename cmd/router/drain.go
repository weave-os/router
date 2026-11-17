package main

import (
	"context"
	"log/slog"
	"net/http"
)

func drainServer(ctx context.Context, srv *http.Server, logger *slog.Logger, waitForBackgroundWork func(context.Context)) {
	if err := srv.Shutdown(ctx); err != nil {
		logger.Error("Graceful shutdown failed", "err", err)
	}
	waitForBackgroundWork(ctx)
}
