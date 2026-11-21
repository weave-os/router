package credentials

import (
	"context"
	"net/http"
)

type SubscriptionCoverage func(ctx context.Context, headers http.Header, routePath string) bool

type CustomerFunding func(ctx context.Context, headers http.Header, routePath string, byokFeeOwed bool) bool
