package observability

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/lmittmann/tint"
	"github.com/mattn/go-isatty"
	"github.com/vlad-tokarev/sloggcp"
)

const ginContextKey = "router_logger"

// loggerContextKey is a private type so context values don't collide with
// other packages'.
type loggerContextKey struct{}

// requestIDContextKey carries the per-request correlation id independently of
// the logger, so code that needs the raw value (telemetry rows, billing
// ledger) reads the same id the logs are tagged with.
type requestIDContextKey struct{}

// WithRequestID attaches a request correlation id to ctx.
func WithRequestID(ctx context.Context, requestID string) context.Context {
	if requestID == "" {
		return ctx
	}
	return context.WithValue(ctx, requestIDContextKey{}, requestID)
}

// RequestIDFromContext returns the correlation id stamped by Middleware, or ""
// when the caller bypassed HTTP (tests, background jobs).
func RequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(requestIDContextKey{}).(string); ok {
		return v
	}
	return ""
}

// requestLoggerHolder is the one logger a request's views share. Middleware
// puts it on both the gin and request contexts; PromoteRequestLogger swaps its
// contents when later-derived fields (session_key) become known, so the access
// log and any FromGin caller pick them up without the proxy having to write a
// new context back onto c.Request.
type requestLoggerHolder struct {
	mu  sync.RWMutex
	log *slog.Logger
}

func (h *requestLoggerHolder) get() *slog.Logger {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.log
}

func (h *requestLoggerHolder) set(log *slog.Logger) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.log = log
}

// holderContextKey carries the shared holder.
type holderContextKey struct{}

// PromoteRequestLogger makes log the request's logger for every view of it:
// the returned context, and the gin context's logger that AccessLog reads.
//
// Without this a logger enriched after Middleware (session_key,
// client_session_id) reaches only the caller's own context, so the guaranteed
// per-request "http request" line stays unfindable by session.
func PromoteRequestLogger(ctx context.Context, log *slog.Logger) context.Context {
	if log == nil {
		return ctx
	}
	if h, ok := ctx.Value(holderContextKey{}).(*requestLoggerHolder); ok {
		h.set(log)
	}
	return WithLogger(ctx, log)
}

// WithLogger attaches a logger to ctx for downstream FromContext calls.
func WithLogger(ctx context.Context, log *slog.Logger) context.Context {
	if log == nil {
		return ctx
	}
	return context.WithValue(ctx, loggerContextKey{}, log)
}

// FromContext returns the logger stashed by WithLogger, or the global default
// if none is set. Always non-nil.
func FromContext(ctx context.Context) *slog.Logger {
	initOnce.Do(initLogger)
	if ctx != nil {
		if v := ctx.Value(loggerContextKey{}); v != nil {
			if logger, ok := v.(*slog.Logger); ok {
				return logger
			}
		}
	}
	return slog.Default()
}

// initOnce installs the LOG_LEVEL-honoring handler on first use; without it
// slog.Default() defaults to INFO and silently drops Debug lines.
var initOnce sync.Once

func initLogger() {
	slog.SetDefault(buildLogger(newHandler(resolveLevel())))
}

// buildLogger attaches the process-wide attributes every line must carry.
// Split from initLogger so tests exercise this instead of restating it.
func buildLogger(h slog.Handler) *slog.Logger {
	logger := slog.New(h)
	// Every line carries the emitting service so a multi-service log sink can
	// be filtered down to this process. NAME is what the deployment already
	// sets per service; OTEL_SERVICE_NAME is the self-hosted fallback.
	if name := serviceName(); name != "" {
		logger = logger.With("name", name)
	}
	return logger
}

// resolveLevel reads the handler level from LOG_LEVEL.
func resolveLevel() slog.Level {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LOG_LEVEL"))) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	}
	return slog.LevelInfo
}

// serviceName resolves the service tag attached to every log line.
func serviceName() string {
	for _, key := range []string{"NAME", "OTEL_SERVICE_NAME"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	return defaultServiceName
}

// defaultServiceName tags lines when the deployment sets no service name, so
// output is never untagged.
const defaultServiceName = "router"

// newHandler builds the slog handler for the resolved format. JSON uses
// sloggcp.ReplaceAttr so lines render correctly in GCP Cloud Logging; tint
// gives colorized output for local dev.
func newHandler(level slog.Level) slog.Handler {
	switch logFormat() {
	case "json":
		return slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
			Level:       level,
			ReplaceAttr: sloggcp.ReplaceAttr,
		})
	case "text":
		return slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	case "tint":
		return tint.NewHandler(os.Stderr, &tint.Options{Level: level, TimeFormat: time.Kitchen})
	}
	// Auto: TTY gets human-readable output (colorized unless disabled); non-TTY
	// gets structured GCP JSON.
	if useColor() {
		return tint.NewHandler(os.Stderr, &tint.Options{Level: level, TimeFormat: time.Kitchen})
	}
	if isTerminal() {
		return slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	}
	return slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level:       level,
		ReplaceAttr: sloggcp.ReplaceAttr,
	})
}

// logFormat returns the requested handler format from LOG_FORMAT
// ({json,text,color,tint}), or "" to auto-detect.
func logFormat() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LOG_FORMAT"))) {
	case "json":
		return "json"
	case "text":
		return "text"
	case "color", "tint":
		return "tint"
	}
	return ""
}

// useColor reports whether auto format should use tint's colorized handler.
// Respects LOG_COLOR={1,true,yes,on}/{0,false,no,off}; otherwise auto-detects
// via TTY + NO_COLOR (https://no-color.org).
func useColor() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LOG_COLOR"))) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return false
	}
	return isTerminal()
}

// isTerminal reports whether stderr is an interactive terminal, independent
// of color preference.
func isTerminal() bool {
	return isatty.IsTerminal(os.Stderr.Fd()) || isatty.IsCygwinTerminal(os.Stderr.Fd())
}

func Get() *slog.Logger {
	initOnce.Do(initLogger)
	return slog.Default()
}

// FromGin returns the request-scoped logger bound by Middleware, including any
// fields promoted later via PromoteRequestLogger. Falls back to the request
// context's logger (then the global default) so a route registered without
// Middleware still logs rather than panicking.
func FromGin(c *gin.Context) *slog.Logger {
	initOnce.Do(initLogger)
	if v, ok := c.Get(ginContextKey); ok {
		if h, ok := v.(*requestLoggerHolder); ok {
			return h.get()
		}
		if logger, ok := v.(*slog.Logger); ok {
			return logger
		}
	}
	if c.Request != nil {
		return FromContext(c.Request.Context())
	}
	return slog.Default()
}

// Middleware binds a request correlation id to both the gin and request
// contexts, so every line served under it filters by request_id alone.
//
// Bound here rather than deeper because the body is still unparsed: first
// touch is what makes pre-parse failures findable at all.
func Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		initOnce.Do(initLogger)

		// Always mint a fresh id; adopting an inbound one lets a client reuse
		// a value across requests and breaks request_id as a unique key.
		requestID := uuid.New().String()

		logger := slog.Default().With(
			"request_id", requestID,
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
		)
		if upstream := sanitizeLogValue(c.Request.Header.Get("X-Request-Id")); upstream != "" {
			logger = logger.With("upstream_request_id", upstream)
		}

		holder := &requestLoggerHolder{log: logger}
		c.Set(ginContextKey, holder)
		c.Header("X-Request-Id", requestID)

		ctx := WithRequestID(c.Request.Context(), requestID)
		ctx = context.WithValue(ctx, holderContextKey{}, holder)
		ctx = WithLogger(ctx, logger)
		c.Request = c.Request.WithContext(ctx)

		c.Next()
	}
}

// maxLoggedHeaderLen bounds a client-supplied header before it reaches a log
// field, so a pathological value can't bloat every line of a request.
const maxLoggedHeaderLen = 200

// sanitizeLogValue trims a caller-controlled header to a bounded, single-line
// value fit for a log field.
func sanitizeLogValue(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	v = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		return r
	}, v)
	if len(v) > maxLoggedHeaderLen {
		return v[:maxLoggedHeaderLen]
	}
	return v
}

// AccessLog logs one INFO line per request after handlers run — without it, a
// 401 from WithAuth produces zero output at LOG_LEVEL=info, masking traffic.
func AccessLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		FromGin(c).Info("http request",
			"status", c.Writer.Status(),
			"latency_ms", time.Since(start).Milliseconds(),
			"client_ip", c.ClientIP(),
		)
	}
}
