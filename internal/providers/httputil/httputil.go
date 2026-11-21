package httputil

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/timing"
)

const FlushChunk = 4 * 1024

var ErrUpstreamIdleTimeout = providers.ErrUpstreamIdleTimeout

var ErrUpstreamOutputStall = providers.ErrUpstreamOutputStall

var ErrUpstreamSlowThroughput = providers.ErrUpstreamSlowThroughput

const (
	defaultSSEIdleBudget          = 45 * time.Second
	defaultResponsesSSEIdleBudget = 90 * time.Second
)

type Config struct {
	SSEIdleTimeout               time.Duration
	ResponsesSSEIdleTimeout      time.Duration
	ResponsesOutputStallTimeout  time.Duration
	OutputStallTimeout           time.Duration
	ThroughputWindow             time.Duration
	ThroughputMinElapsed         time.Duration
	MinThroughputDeltasPerWindow int
	H2ReadIdleTimeout            time.Duration
	H2PingTimeout                time.Duration
	PublicDestinationsOnly       bool
}

func DefaultConfig() Config {
	return Config{
		SSEIdleTimeout:               defaultSSEIdleBudget,
		ResponsesSSEIdleTimeout:      defaultResponsesSSEIdleBudget,
		ResponsesOutputStallTimeout:  240 * time.Second,
		OutputStallTimeout:           240 * time.Second,
		ThroughputWindow:             20 * time.Second,
		ThroughputMinElapsed:         90 * time.Second,
		MinThroughputDeltasPerWindow: 8,
		H2ReadIdleTimeout:            15 * time.Second,
		H2PingTimeout:                5 * time.Second,
	}
}

var DefaultSSEIdleTimeout = DefaultConfig().SSEIdleTimeout

var DefaultResponsesSSEIdleTimeout = DefaultConfig().ResponsesSSEIdleTimeout

var DefaultResponsesOutputStallTimeout = DefaultConfig().ResponsesOutputStallTimeout

var DefaultOutputStallTimeout = DefaultConfig().OutputStallTimeout

var (
	DefaultThroughputWindow = DefaultConfig().ThroughputWindow

	DefaultThroughputMinElapsed = DefaultConfig().ThroughputMinElapsed

	DefaultMinThroughputDeltasPerWindow = DefaultConfig().MinThroughputDeltasPerWindow
)

func Configure(cfg Config) {
	DefaultSSEIdleTimeout = cfg.SSEIdleTimeout
	DefaultResponsesSSEIdleTimeout = cfg.ResponsesSSEIdleTimeout
	DefaultResponsesOutputStallTimeout = cfg.ResponsesOutputStallTimeout
	DefaultOutputStallTimeout = cfg.OutputStallTimeout
	DefaultThroughputWindow = cfg.ThroughputWindow
	DefaultThroughputMinElapsed = cfg.ThroughputMinElapsed
	DefaultMinThroughputDeltasPerWindow = cfg.MinThroughputDeltasPerWindow
	DefaultH2ReadIdleTimeout = cfg.H2ReadIdleTimeout
	DefaultH2PingTimeout = cfg.H2PingTimeout
	publicDestinationsOnly = cfg.PublicDestinationsOnly
}

func ParseInt(raw string, fallback int) int {
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return n
}

func ParseTimeoutSeconds(raw string, fallback time.Duration) time.Duration {
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return fallback
	}
	return time.Duration(n) * time.Second
}

const DefaultResponseHeaderTimeout = 30 * time.Second

var DefaultH2ReadIdleTimeout = DefaultConfig().H2ReadIdleTimeout

var DefaultH2PingTimeout = DefaultConfig().H2PingTimeout

const h2KeepaliveNumerator, h2KeepaliveDenominator = 2, 3

func h2KeepaliveFor(responseHeaderTimeout time.Duration) (readIdle, ping time.Duration) {
	readIdle, ping = DefaultH2ReadIdleTimeout, DefaultH2PingTimeout
	if responseHeaderTimeout <= 0 {
		return readIdle, ping
	}
	budget := responseHeaderTimeout * h2KeepaliveNumerator / h2KeepaliveDenominator
	if readIdle+ping <= budget {
		return readIdle, ping
	}
	scale := float64(budget) / float64(readIdle+ping)
	scaledIdle, scaledPing := time.Duration(float64(readIdle)*scale), time.Duration(float64(ping)*scale)
	observability.Get().Warn("HTTP/2 keepalive budget exceeds the response-header guard, scaling to fit",
		"response_header_timeout_ms", responseHeaderTimeout.Milliseconds(),
		"configured_read_idle_ms", readIdle.Milliseconds(),
		"configured_ping_ms", ping.Milliseconds(),
		"effective_read_idle_ms", scaledIdle.Milliseconds(),
		"effective_ping_ms", scaledPing.Milliseconds())
	return scaledIdle, scaledPing
}

func SanitizeInboundAuthHeader(v string) string {
	const bearerPrefix = "Bearer "
	if len(v) > len(bearerPrefix) && strings.EqualFold(v[:len(bearerPrefix)], bearerPrefix) {
		if auth.HasAPIKeyPrefix(strings.TrimSpace(v[len(bearerPrefix):])) {
			return ""
		}
	}
	return v
}

func NewTransport(dialTimeout, tlsTimeout time.Duration) *http.Transport {
	return NewTransportWithResponseHeaderTimeout(dialTimeout, tlsTimeout, DefaultResponseHeaderTimeout)
}

func NewTransportWithResponseHeaderTimeout(dialTimeout, tlsTimeout, responseHeaderTimeout time.Duration) *http.Transport {
	return newTransport(dialTimeout, tlsTimeout, responseHeaderTimeout, publicDestinationsOnly)
}

func newTransport(dialTimeout, tlsTimeout, responseHeaderTimeout time.Duration, publicOnly bool) *http.Transport {
	dialer := &net.Dialer{
		Timeout:   dialTimeout,
		KeepAlive: 30 * time.Second,
	}
	proxy := http.ProxyFromEnvironment
	if publicOnly {
		dialer.Control = restrictDestination
		proxy = nil
	}
	sendPingAfter, pingTimeout := h2KeepaliveFor(responseHeaderTimeout)
	return &http.Transport{
		Proxy:                 proxy,
		DialContext:           dialer.DialContext,
		MaxIdleConnsPerHost:   64,
		MaxIdleConns:          256,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   tlsTimeout,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: responseHeaderTimeout,
		ForceAttemptHTTP2:     true,
		HTTP2: &http.HTTP2Config{
			SendPingTimeout: sendPingAfter,
			PingTimeout:     pingTimeout,
		},
	}
}

var ErrRefusedRedirect = errors.New("upstream returned a redirect, which is not followed")

func NewClient(transport http.RoundTripper) *http.Client {
	if transport == nil {
		transport = http.DefaultTransport
	}
	transport = &trafficCaptureRoundTripper{transport: transport}
	return &http.Client{Transport: observability.NewTracingTransport(transport), CheckRedirect: refuseRedirect}
}

func refuseRedirect(*http.Request, []*http.Request) error {
	return ErrRefusedRedirect
}

func StartIdleWatchdog(ctx context.Context, cancel context.CancelCauseFunc, idleTimeout time.Duration) (mark func(), stop func()) {
	return StartIdleWatchdogCause(ctx, cancel, idleTimeout, ErrUpstreamIdleTimeout)
}

func StartIdleWatchdogCause(ctx context.Context, cancel context.CancelCauseFunc, idleTimeout time.Duration, cause error) (mark func(), stop func()) {
	if idleTimeout <= 0 || cancel == nil {
		return func() {}, func() {}
	}
	var mu sync.Mutex
	lastProgress := time.Now()
	stopped := false
	done := make(chan struct{})
	go func() {
		timer := time.NewTimer(idleTimeout)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-timer.C:
				mu.Lock()
				if stopped {
					mu.Unlock()
					return
				}
				remaining := idleTimeout - time.Since(lastProgress)
				if remaining <= 0 {
					stopped = true
					cancel(cause)
					mu.Unlock()
					return
				}
				mu.Unlock()
				timer.Reset(remaining)
			}
		}
	}()
	return func() {
			mu.Lock()
			lastProgress = time.Now()
			mu.Unlock()
		}, sync.OnceFunc(func() {
			mu.Lock()
			stopped = true
			mu.Unlock()
			close(done)
		})
}

func StartThroughputWatchdog(ctx context.Context, cancel context.CancelCauseFunc, window, minElapsed time.Duration, minDeltas int, cause error) (mark func(), stop func()) {
	if minDeltas <= 0 || window <= 0 || cancel == nil {
		return func() {}, func() {}
	}
	var count atomic.Int64
	start := time.Now()
	floorRate := float64(minDeltas) / window.Seconds()
	done := make(chan struct{})
	go func() {
		interval := min(max(window/4, 50*time.Millisecond), 5*time.Second)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		var windowStartCount int64
		windowStart := start
		subFloorStreak := 0
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-ticker.C:
				now := time.Now()
				if now.Sub(start) < minElapsed {
					windowStartCount = count.Load()
					windowStart = now
					continue
				}
				elapsed := now.Sub(windowStart)
				if elapsed < window {
					continue
				}
				cur := count.Load()
				marks := cur - windowStartCount
				rate := float64(marks) / elapsed.Seconds()
				if marks > 0 && rate < floorRate {
					subFloorStreak++
					if subFloorStreak >= 2 {
						cancel(cause)
						return
					}
				} else {
					subFloorStreak = 0
				}
				windowStartCount = cur
				windowStart = now
			}
		}
	}()
	return func() { count.Add(1) },
		sync.OnceFunc(func() { close(done) })
}

type StreamOption func(*streamConfig)

type streamConfig struct {
	onChunk func(chunk []byte, first bool)
}

func WithOnChunk(fn func(chunk []byte, first bool)) StreamOption {
	return func(c *streamConfig) { c.onChunk = fn }
}

func StreamBody(ctx context.Context, cancel context.CancelCauseFunc, idleTimeout time.Duration, r io.Reader, status int, w http.ResponseWriter, t *timing.Timing, opts ...StreamOption) error {
	var cfg streamConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	mark, stop := StartIdleWatchdog(ctx, cancel, idleTimeout)
	defer stop()

	flusher, _ := w.(http.Flusher)
	buf := make([]byte, FlushChunk)
	first := true
	for {
		n, readErr := r.Read(buf)
		if n > 0 {
			mark()
			t.StampUpstreamFirstByte()
			if cfg.onChunk != nil {
				cfg.onChunk(buf[:n], first)
				first = false
			}
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				return writeErr
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if readErr == io.EOF {
			t.StampUpstreamEOF()
			if status < 200 || status >= 300 {
				return &providers.UpstreamStatusError{Status: status}
			}
			return nil
		}
		if readErr != nil {
			if cause := context.Cause(ctx); errors.Is(cause, ErrUpstreamIdleTimeout) || errors.Is(cause, ErrUpstreamOutputStall) || errors.Is(cause, ErrUpstreamSlowThroughput) {
				return cause
			}
			return readErr
		}
	}
}
