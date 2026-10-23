package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/timing"

	"github.com/gin-gonic/gin"
)

// ProviderWaitHeader opts a local UI bridge into the framed response transport.
const ProviderWaitHeader = "X-Weave-Provider-Wait"

// ProviderWaitContentType identifies frames carrying wait state and the original
// HTTP response. Enclosing that response preserves pre-output HTTP errors while
// allowing dispatch notifications to reach the bridge before response headers.
const ProviderWaitContentType = "application/vnd.weave.provider-wait+ndjson"

type providerWaitFrameKind string

const (
	providerWaitFrameWaiting  providerWaitFrameKind = "waiting"
	providerWaitFrameResponse providerWaitFrameKind = "response"
	providerWaitFrameBody     providerWaitFrameKind = "body"
	providerWaitFrameDone     providerWaitFrameKind = "done"
)

type providerWaitFrame struct {
	Type    providerWaitFrameKind `json:"type"`
	Waiting bool                  `json:"waiting,omitempty"`
	Status  int                   `json:"status,omitempty"`
	Headers http.Header           `json:"headers,omitempty"`
	Body    []byte                `json:"body,omitempty"`
}

// WithProviderWait wraps only opted-in inference requests. Ordinary clients keep
// their native wire protocol; the bridge reconstructs it from these frames.
func WithProviderWait() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.GetHeader(ProviderWaitHeader) != "1" {
			c.Next()
			return
		}
		c.Request.Header.Del(ProviderWaitHeader)
		ctx, cancel := context.WithCancelCause(c.Request.Context())
		defer cancel(nil)
		writer := &providerWaitWriter{
			ResponseWriter: c.Writer,
			headers:        c.Writer.Header().Clone(),
			status:         http.StatusOK,
			size:           -1,
			cancel:         cancel,
		}
		c.Writer.Header().Del("Content-Length")
		c.Writer.Header().Set("Content-Type", ProviderWaitContentType)
		c.Writer.Header().Set("Cache-Control", "no-store")
		c.Writer = writer
		c.Request = c.Request.WithContext(timing.WithProviderWaitObserver(ctx, writer.observe))
		c.Next()
		writer.finish()
		if writer.err != nil {
			observability.FromGin(c).Debug("Provider wait bridge disconnected", "err", writer.err)
		}
	}
}

type providerWaitWriter struct {
	gin.ResponseWriter
	mu      sync.Mutex
	headers http.Header
	status  int
	size    int
	waiting bool
	err     error
	cancel  context.CancelCauseFunc
}

func (w *providerWaitWriter) Header() http.Header { return w.headers }
func (w *providerWaitWriter) Status() int         { return w.status }
func (w *providerWaitWriter) Size() int           { return w.size }
func (w *providerWaitWriter) Written() bool       { return w.size >= 0 }

func (w *providerWaitWriter) WriteHeader(status int) {
	if !w.Written() {
		w.status = status
	}
}

func (w *providerWaitWriter) WriteHeaderNow() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writeHeaders()
}

func (w *providerWaitWriter) writeHeaders() {
	if !w.Written() {
		w.emit(providerWaitFrame{Type: providerWaitFrameResponse, Status: w.status, Headers: w.headers})
		w.size = 0
	}
}

func (w *providerWaitWriter) Write(body []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writeHeaders()
	w.emit(providerWaitFrame{Type: providerWaitFrameBody, Body: body})
	if w.err != nil {
		return 0, w.err
	}
	w.size += len(body)
	return len(body), nil
}

func (w *providerWaitWriter) WriteString(body string) (int, error) {
	return w.Write([]byte(body))
}

func (w *providerWaitWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writeHeaders()
	w.ResponseWriter.Flush()
}

func (w *providerWaitWriter) observe(waiting bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if waiting != w.waiting {
		w.waiting = waiting
		w.emit(providerWaitFrame{Type: providerWaitFrameWaiting, Waiting: waiting})
	}
}

func (w *providerWaitWriter) emit(frame providerWaitFrame) {
	if w.err != nil {
		return
	}
	if w.err = json.NewEncoder(w.ResponseWriter).Encode(frame); w.err != nil {
		w.cancel(w.err)
		return
	}
	w.ResponseWriter.Flush()
}

func (w *providerWaitWriter) finish() {
	w.observe(false)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writeHeaders()
	w.emit(providerWaitFrame{Type: providerWaitFrameDone})
}

var _ gin.ResponseWriter = (*providerWaitWriter)(nil)
