package proxy

import (
	"context"
	"encoding/json"
	"net/http"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/proxy/usage"
)

// unifiedLimitCapture is a request-scoped holder for the raw subscription
// limit headers: anthropic-ratelimit-unified-* for Claude and the x-codex-*
// quota/credit set for Codex. Instrumentation only — nothing reads it except
// the telemetry row builder and the overage classifier; no effect on routing,
// subsidy, or usage-bypass.
//
// Retry/failover: captures the LAST subscription-served attempt's headers —
// a 429'd subscription retry that falls over to the deployment key leaves the
// near-cap reading here. Both families are replaced on every attempt, so a
// Claude attempt followed by a Codex attempt leaves only the Codex reading.
// Consumers disambiguate via failover_used + credential_source.
type unifiedLimitCapture struct {
	raw   map[string]string
	codex map[string]string
}

type unifiedLimitCaptureKey struct{}

// withUnifiedLimitCapture installs an empty capture holder on ctx. The holder
// is a pointer, so the header observer mutates the same struct the caller
// reads later via UnifiedLimitHeadersFrom — no extra plumbing needed.
func withUnifiedLimitCapture(ctx context.Context) context.Context {
	return context.WithValue(ctx, unifiedLimitCaptureKey{}, &unifiedLimitCapture{})
}

// captureUnifiedLimitHeaders records the raw subscription limit headers on
// ctx's holder when the resolved credential is OAuth (subscription).
// Skipped for BYOK/deployment calls — those headers describe a different account.
func captureUnifiedLimitHeaders(ctx context.Context, h http.Header) {
	c, ok := ctx.Value(unifiedLimitCaptureKey{}).(*unifiedLimitCapture)
	if !ok || c == nil {
		return
	}
	creds := CredentialsFromContext(ctx)
	if creds == nil || !creds.OAuth {
		return
	}
	c.raw = usage.RawAnthropicUnifiedHeaders(h)
	c.codex = usage.RawCodexLimitHeaders(h)
}

// UnifiedLimitHeadersFrom returns the raw Anthropic header map captured for
// this request (nil if withUnifiedLimitCapture was never installed, or nothing
// was captured — e.g. no subscription call was made this turn).
func UnifiedLimitHeadersFrom(ctx context.Context) map[string]string {
	c, ok := ctx.Value(unifiedLimitCaptureKey{}).(*unifiedLimitCapture)
	if !ok || c == nil {
		return nil
	}
	return c.raw
}

// CodexLimitHeadersFrom is the Codex counterpart of UnifiedLimitHeadersFrom.
func CodexLimitHeadersFrom(ctx context.Context) map[string]string {
	c, ok := ctx.Value(unifiedLimitCaptureKey{}).(*unifiedLimitCapture)
	if !ok || c == nil {
		return nil
	}
	return c.codex
}

// unifiedLimitHeadersJSON marshals the captured unified-header map to []byte
// for InsertTelemetryParams. Returns nil (not empty JSON object) when nothing
// was captured, so the column stays NULL.
func unifiedLimitHeadersJSON(ctx context.Context) []byte {
	return limitHeadersJSON(ctx, UnifiedLimitHeadersFrom(ctx), "unified_limit_headers")
}

// codexLimitHeadersJSON is the codex_limit_headers counterpart of
// unifiedLimitHeadersJSON.
func codexLimitHeadersJSON(ctx context.Context) []byte {
	return limitHeadersJSON(ctx, CodexLimitHeadersFrom(ctx), "codex_limit_headers")
}

func limitHeadersJSON(ctx context.Context, raw map[string]string, column string) []byte {
	if len(raw) == 0 {
		return nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		// Telemetry loss is acceptable; a marshal failure must never fail the
		// request, so log and leave the column NULL.
		observability.FromContext(ctx).Debug("Failed to marshal limit headers for telemetry", "column", column, "err", err)
		return nil
	}
	return b
}
