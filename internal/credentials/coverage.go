package credentials

import (
	"context"
	"net/http"
)

type SubscriptionCoverage func(ctx context.Context, headers http.Header, routePath string) bool
