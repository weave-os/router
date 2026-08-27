package httputil

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"workweave/router/internal/observability"
	"workweave/router/internal/providers"
)

// ReadCapped buffers up to limit bytes from r, then drains (without retaining)
// up to maxDrain more to bound failover latency on a large error body. Returns
// the buffered prefix, total bytes read, and any read error (io.EOF -> nil).
func ReadCapped(r io.Reader, limit int) ([]byte, int64, error) {
	prefix, err := io.ReadAll(io.LimitReader(r, int64(limit)))
	totalRead := int64(len(prefix))
	if err != nil {
		return prefix, totalRead, err
	}
	const maxDrain = 1 << 20 // 1 MiB
	rest, drainErr := io.Copy(io.Discard, io.LimitReader(r, maxDrain))
	totalRead += rest
	return prefix, totalRead, drainErr
}

// PreviewBytes returns the first 1KB of body as a string for logging.
func PreviewBytes(body []byte) string {
	const previewLimit = 1024
	if len(body) > previewLimit {
		return string(body[:previewLimit])
	}
	return string(body)
}

// HeaderCapture is a minimal http.ResponseWriter that captures headers only,
// used to reuse providers.CopyUpstreamHeaders against an http.Header we own.
// Write/WriteHeader are no-ops.
type HeaderCapture struct{ H http.Header }

// Header returns the captured header set.
func (c HeaderCapture) Header() http.Header { return c.H }

// Write is a no-op; HeaderCapture only captures headers.
func (c HeaderCapture) Write([]byte) (int, error) { return 0, nil }

// WriteHeader is a no-op; HeaderCapture only captures headers.
func (c HeaderCapture) WriteHeader(int) {}

// LogUpstreamStatus logs non-2xx upstream responses with a body preview, at
// ERROR except 429 (routine rate-limit signal handled via failover), which
// logs at WARN.
//
// ctx is load-bearing: on the global logger the body was written but not
// joinable to the request, so filtering by session never surfaced it.
func LogUpstreamStatus(ctx context.Context, msg string, status int, attrs ...any) {
	log := observability.FromContext(ctx)
	merged := append([]any{"status", status}, attrs...)
	if status >= 500 || (status >= 400 && status != http.StatusTooManyRequests) {
		log.Error(msg, merged...)
		return
	}
	log.Warn(msg, merged...)
}

// IsRedirect reports whether status is a 3xx. Provider clients refuse to
// follow redirects (NewClient), so adapters must catch the returned 3xx and
// treat it as an upstream error — relaying it would hand the Location to the
// client, which follows it carrying its own credential and the prompt body,
// re-opening the exact leak refusing the redirect was meant to close.
func IsRedirect(status int) bool {
	return status >= 300 && status < 400
}

// refusedRedirectBody is the synthesized client-facing envelope for a refused
// upstream 3xx. The Location is deliberately omitted: it may carry signed
// query tokens and pointing the client at it defeats the refusal.
func refusedRedirectBody(status int) []byte {
	return []byte(fmt.Sprintf(`{"error":{"message":%q,"type":"api_error"}}`,
		fmt.Sprintf("upstream returned a redirect (status %d); the router does not follow redirects", status)))
}

// drainRedirect discards a refused 3xx's body (bounded) so the connection can
// be reused; redirect bodies are boilerplate and never surfaced.
func drainRedirect(body io.Reader) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, 64*1024))
}

// logRefusedRedirect logs at ERROR (not LogUpstreamStatus, which would rank a
// 3xx below a 4xx): a redirecting upstream means a misconfigured or hostile
// base URL and produces zero other log lines, so this line is the only trace.
func logRefusedRedirect(ctx context.Context, msg string, resp *http.Response, attrs ...any) {
	merged := append([]any{"status", resp.StatusCode, "location", resp.Header.Get("Location")}, attrs...)
	observability.FromContext(ctx).Error(msg, merged...)
}

// RefusedRedirectError converts a refused upstream 3xx into a synthesized
// buffered 502 (*providers.UpstreamErrorResponse) for the routed dispatch
// paths: retryable, so the failover loop can try another binding, and
// carrying none of the upstream's headers, so the Location can never reach
// the client via flushBufferedIfPresent.
func RefusedRedirectError(ctx context.Context, resp *http.Response, msg string, attrs ...any) error {
	drainRedirect(resp.Body)
	logRefusedRedirect(ctx, msg, resp, attrs...)
	return &providers.UpstreamErrorResponse{
		Status: http.StatusBadGateway,
		Body:   refusedRedirectBody(resp.StatusCode),
	}
}

// WriteRefusedRedirect renders a refused upstream 3xx as a 502 JSON envelope
// on w for the passthrough paths (no failover loop to buffer for), copying
// none of the upstream's headers, and returns *providers.UpstreamStatusError
// so callers treat the response as already written.
func WriteRefusedRedirect(ctx context.Context, w http.ResponseWriter, resp *http.Response, msg string, attrs ...any) error {
	drainRedirect(resp.Body)
	logRefusedRedirect(ctx, msg, resp, attrs...)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadGateway)
	_, _ = w.Write(refusedRedirectBody(resp.StatusCode))
	return &providers.UpstreamStatusError{Status: http.StatusBadGateway}
}

// WritePassthroughError streams up to 1KB of resp.Body to w, logs via
// LogUpstreamStatus (body_preview/body_total_bytes appended automatically),
// and returns UpstreamStatusError. onFirstByte/onEOF are nil-safe OTel
// timing hooks; pass nil, nil on paths that don't stamp upstream timing.
func WritePassthroughError(ctx context.Context, w http.ResponseWriter, resp *http.Response, onFirstByte, onEOF func(), msg string, attrs ...any) error {
	var snip [1024]byte
	n, _ := io.ReadFull(resp.Body, snip[:])
	if n > 0 && onFirstByte != nil {
		onFirstByte()
	}
	_, snipWriteErr := w.Write(snip[:n])
	rest, copyErr := io.Copy(w, resp.Body)
	if copyErr == nil && onEOF != nil {
		onEOF()
	}
	merged := append(append([]any{}, attrs...), "body_preview", string(snip[:n]), "body_total_bytes", int64(n)+rest)
	LogUpstreamStatus(ctx, msg, resp.StatusCode, merged...)
	if snipWriteErr != nil {
		return snipWriteErr
	}
	if copyErr != nil {
		return copyErr
	}
	return &providers.UpstreamStatusError{Status: resp.StatusCode}
}
