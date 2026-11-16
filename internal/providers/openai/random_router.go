package openai

import (
	"context"
	"net/http"
	"weave-os/router/internal/requestcontext"
)

func includedSubscriptionRequest(ctx context.Context, request *http.Request) *http.Request {
	if requestcontext.IncludedSubscriptionOnlyFrom(ctx) {
		request.GetBody = nil
		request.Close = true
	}
	return request
}
