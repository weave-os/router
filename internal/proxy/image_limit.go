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

type requestShape struct {
	imageCount int
	bodyBytes  int
}

func withRequestShape(ctx context.Context, imageCount, bodyBytes int) context.Context {
	return context.WithValue(ctx, requestImageCountContextKey{}, requestShape{imageCount: imageCount, bodyBytes: bodyBytes})
}

func withRequestImageCount(ctx context.Context, n int) context.Context {
	return withRequestShape(ctx, n, requestBodyBytesFromContext(ctx))
}

func requestImageCountFromContext(ctx context.Context) int {
	shape, _ := ctx.Value(requestImageCountContextKey{}).(requestShape)
	return shape.imageCount
}

func requestBodyBytesFromContext(ctx context.Context) int {
	shape, _ := ctx.Value(requestImageCountContextKey{}).(requestShape)
	return shape.bodyBytes
}

func withRequestBodyBytes(ctx context.Context, req router.Request) router.Request {
	req.RequestBodyBytes = requestBodyBytesFromContext(ctx)
	return req
}
