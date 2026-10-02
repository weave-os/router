package dispatch

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
)

// Buffered describes a non-streaming auxiliary call whose whole upstream
// response is captured before the caller reads it. Prepare builds the wire
// request for the attempt's target; Consume parses the recorded response.
// The provider client is dereferenced only here, so feature packages never
// call providers.Client directly.
type Buffered struct {
	Prepare func(context.Context, Attempt) (providers.PreparedRequest, *http.Request, error)
	Consume func(context.Context, Attempt, *http.Response) error
	// Reason is recorded on the router.Decision handed to the provider.
	Reason string
	// MaxResponseBytes bounds retained output when positive.
	MaxResponseBytes int
}

// ErrBufferedResponseTooLarge means an auxiliary response exceeded its allocation budget.
var ErrBufferedResponseTooLarge = errors.New("buffered inference response exceeded its byte budget")

type boundedResponseWriter struct {
	recorder  *httptest.ResponseRecorder
	remaining int
	overflow  bool
}

func (w *boundedResponseWriter) Header() http.Header { return w.recorder.Header() }

func (w *boundedResponseWriter) WriteHeader(status int) { w.recorder.WriteHeader(status) }

func (w *boundedResponseWriter) Write(payload []byte) (int, error) {
	if len(payload) > w.remaining {
		w.overflow = true
		return 0, ErrBufferedResponseTooLarge
	}
	w.remaining -= len(payload)
	return w.recorder.Write(payload)
}

// Transport adapts b into an executor Transport. The prepared wire model must
// name the attempt's target or the attempt fails with ErrTargetMismatch
// before any upstream I/O.
func (b Buffered) Transport() Transport {
	return Transport{Attempt: func(ctx context.Context, attempt Attempt, client providers.Client) error {
		prep, req, err := b.Prepare(ctx, attempt)
		if err != nil {
			return err
		}
		if err := ValidatePreparedTarget(prep, attempt.Target); err != nil {
			return err
		}
		rec := httptest.NewRecorder()
		var writer http.ResponseWriter = rec
		var bounded *boundedResponseWriter
		if b.MaxResponseBytes > 0 {
			bounded = &boundedResponseWriter{recorder: rec, remaining: b.MaxResponseBytes}
			writer = bounded
		}
		decision := router.Decision{
			Provider: attempt.Target.Provider,
			Model:    attempt.Target.CatalogID,
			Reason:   b.Reason,
		}
		if err := client.Proxy(ctx, decision, prep, writer, req); err != nil {
			return err
		}
		if bounded != nil && bounded.overflow {
			return ErrBufferedResponseTooLarge
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if b.Consume == nil {
			return nil
		}
		return b.Consume(ctx, attempt, rec.Result())
	}}
}
