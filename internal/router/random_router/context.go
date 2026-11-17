package randomrouter

import (
	"context"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/router"
)

type RequestState struct {
	Config            Config
	Policy            router.Router
	CapacityAccountID string
	ProviderCalls     int
	SelectedModel     string
	AttemptUsage      inference.Usage
	AttemptCostUSD    float64
	Prepared          bool
}

type requestStateKey struct{}

func WithRequest(ctx context.Context, request *RequestState) context.Context {
	return context.WithValue(ctx, requestStateKey{}, request)
}

func RequestFrom(ctx context.Context) *RequestState {
	request, _ := ctx.Value(requestStateKey{}).(*RequestState)
	return request
}
