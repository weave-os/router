package observability

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/lmittmann/tint"
	"github.com/mattn/go-isatty"
	"github.com/vlad-tokarev/sloggcp"
)

const ginContextKey = "router_logger"

type loggerContextKey struct{}

type requestIDContextKey struct{}

func WithRequestID(ctx context.Context, requestID string) context.Context {
	if requestID == "" {
		return ctx
	}
	return context.WithValue(ctx, requestIDContextKey{}, requestID)
}

func RequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(requestIDContextKey{}).(string); ok {
		return v
	}
	return ""
}

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

type holderContextKey struct{}

func PromoteRequestLogger(ctx context.Context, log *slog.Logger) context.Context {
	if log == nil {
		return ctx
	}
	if h, ok := ctx.Value(holderContextKey{}).(*requestLoggerHolder); ok {
		h.set(log)
	}
	return WithLogger(ctx, log)
}

func WithLogger(ctx context.Context, log *slog.Logger) context.Context {
	if log == nil {
		return ctx
	}
	return context.WithValue(ctx, loggerContextKey{}, log)
}

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

var initOnce sync.Once

type ColorMode int

const (
	ColorAuto ColorMode = iota
	ColorOn
	ColorOff
)

type Config struct {
	Level        slog.Level
	ServiceName  string
	Format       string
	Color        ColorMode
	GCPProjectID string
}

var (
	activeConfig    atomic.Pointer[Config]
	loggerInstalled atomic.Bool
)

func Configure(cfg Config) {
	activeConfig.Store(&cfg)
	if loggerInstalled.Load() {
		installLogger(cfg)
	}
}

func currentConfig() Config {
	if cfg := activeConfig.Load(); cfg != nil {
		return *cfg
	}
	return Config{}
}

func initLogger() {
	installLogger(currentConfig())
	loggerInstalled.Store(true)
}

func installLogger(cfg Config) {
	slog.SetDefault(buildLogger(newHandler(cfg), cfg.ServiceName))
}

func buildLogger(h slog.Handler, name string) *slog.Logger {
	if name == "" {
		name = defaultServiceName
	}
	return slog.New(h).With("name", name)
}

func ParseLevel(raw string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	}
	return slog.LevelInfo
}

func ResolveServiceName(candidates ...string) string {
	for _, v := range candidates {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return defaultServiceName
}

const defaultServiceName = "router"

func newHandler(cfg Config) slog.Handler {
	level := cfg.Level
	switch cfg.Format {
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
	if useColor(cfg.Color) {
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

func ParseFormat(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "json":
		return "json"
	case "text":
		return "text"
	case "color", "tint":
		return "tint"
	}
	return ""
}

func ParseColor(logColor string, noColorSet bool) ColorMode {
	switch strings.ToLower(strings.TrimSpace(logColor)) {
	case "1", "true", "yes", "on":
		return ColorOn
	case "0", "false", "no", "off":
		return ColorOff
	}
	if noColorSet {
		return ColorOff
	}
	return ColorAuto
}

func useColor(mode ColorMode) bool {
	switch mode {
	case ColorOn:
		return true
	case ColorOff:
		return false
	}
	return isTerminal()
}

func isTerminal() bool {
	return isatty.IsTerminal(os.Stderr.Fd()) || isatty.IsCygwinTerminal(os.Stderr.Fd())
}

func Get() *slog.Logger {
	initOnce.Do(initLogger)
	return slog.Default()
}

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

func Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		initOnce.Do(initLogger)

		requestID := uuid.New().String()

		logger := slog.Default().With(
			"request_id", requestID,
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
		)
		logger = LoggerWithTraceContext(c.Request.Context(), logger)
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

const maxLoggedHeaderLen = 200

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
