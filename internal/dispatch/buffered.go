package dispatch

import (
	"context"
	"net/http"
	"net/http/httptest"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
)

type Buffered struct {
	Prepare func(context.Context, Attempt) (providers.PreparedRequest, *http.Request, error)
	Consume func(context.Context, Attempt, *http.Response) error
	Reason  string
}

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
