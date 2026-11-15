package proxy

import (
	"context"

	"weave-os/router/internal/router"
)

type requestImageCountContextKey struct{}

func readmitForcedImageLimit(req router.Request, model string) router.Request {
	if _, imageLimitExcluded := req.ImageLimitExcludedModels[model]; !imageLimitExcluded {
		return req
	}
	req.ExcludedModels = withoutModels(req.ExcludedModels, []string{model})
	req.ImageCount = 0
	return req
}

func withRequestImageCount(ctx context.Context, n int) context.Context {
	return context.WithValue(ctx, requestImageCountContextKey{}, n)
}

func requestImageCountFromContext(ctx context.Context) int {
	count, _ := ctx.Value(requestImageCountContextKey{}).(int)
	return count
}
