package proxy

import (
	"context"
	"encoding/json"
	"net/http"

	"weave-os/router/internal/credentials"
	"weave-os/router/internal/credentials/usage"
	"weave-os/router/internal/observability"
)

type unifiedLimitCapture struct {
	raw   map[string]string
	codex map[string]string
}

type unifiedLimitCaptureKey struct{}

func withUnifiedLimitCapture(ctx context.Context) context.Context {
	return context.WithValue(ctx, unifiedLimitCaptureKey{}, &unifiedLimitCapture{})
}

func captureUnifiedLimitHeaders(ctx context.Context, h http.Header) {
	c, ok := ctx.Value(unifiedLimitCaptureKey{}).(*unifiedLimitCapture)
	if !ok || c == nil {
		return
	}
	creds := credentials.CredentialsFromContext(ctx)
	if creds == nil || !creds.OAuth {
		return
	}
	c.raw = usage.RawAnthropicUnifiedHeaders(h)
	c.codex = usage.RawCodexLimitHeaders(h)
}

func UnifiedLimitHeadersFrom(ctx context.Context) map[string]string {
	c, ok := ctx.Value(unifiedLimitCaptureKey{}).(*unifiedLimitCapture)
	if !ok || c == nil {
		return nil
	}
	return c.raw
}

func CodexLimitHeadersFrom(ctx context.Context) map[string]string {
	c, ok := ctx.Value(unifiedLimitCaptureKey{}).(*unifiedLimitCapture)
	if !ok || c == nil {
		return nil
	}
	return c.codex
}

func unifiedLimitHeadersJSON(ctx context.Context) []byte {
	return limitHeadersJSON(ctx, UnifiedLimitHeadersFrom(ctx), "unified_limit_headers")
}

func codexLimitHeadersJSON(ctx context.Context) []byte {
	return limitHeadersJSON(ctx, CodexLimitHeadersFrom(ctx), "codex_limit_headers")
}

func limitHeadersJSON(ctx context.Context, raw map[string]string, column string) []byte {
	if len(raw) == 0 {
		return nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		observability.FromContext(ctx).Debug("Failed to marshal limit headers for telemetry", "column", column, "err", err)
		return nil
	}
	return b
}
