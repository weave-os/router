package dispatch

import (
	"context"
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
		decision := router.Decision{
			Provider: attempt.Target.Provider,
			Model:    attempt.Target.CatalogID,
			Reason:   b.Reason,
		}
		if err := client.Proxy(ctx, decision, prep, rec, req); err != nil {
			return err
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
