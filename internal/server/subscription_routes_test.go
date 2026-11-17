package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/server/middleware"
	"weave-os/router/internal/subscriptions"
)

type refreshRouteAccounts struct {
	auth.SubscriptionAccountRepository
}

type refreshDeadlineProbe struct{ remaining time.Duration }

func (p *refreshDeadlineProbe) RefreshAccount(ctx context.Context, _ auth.SubscriptionOwner, id string) (subscriptions.AccountRefreshResult, error) {
	deadline, _ := ctx.Deadline()
	p.remaining = time.Until(deadline)
	return subscriptions.AccountRefreshResult{AccountID: id}, nil
}

func TestInternalRefreshRouteRetainsAuthAndAllowsTokenExchangeBudget(t *testing.T) {
	const token = "synthetic-internal-service-token"
	engine := gin.New()
	authSvc := auth.NewService(nil, nil, nil, nil, auth.NoOpAPIKeyCache{}, nil, time.Now).
		WithSubscriptionAccounts(refreshRouteAccounts{})
	group := engine.Group("/internal/v1", middleware.WithTimeout(adminTimeout), middleware.WithInternalServiceAuth(token))
	probe := &refreshDeadlineProbe{}
	registerInternalSubscriptions(engine, group, authSvc, Features{SubscriptionRefresh: probe}, token)
	path := "/internal/v1/subscription-accounts/11111111-1111-1111-1111-111111111111/22222222-2222-2222-2222-222222222222/refresh"
	unauthenticated := httptest.NewRecorder()
	engine.ServeHTTP(unauthenticated, httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, nil))
	require.Equal(t, http.StatusUnauthorized, unauthenticated.Code)
	require.Zero(t, probe.remaining)
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, nil)
	request.Header.Set("X-Weave-Internal-Token", token)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, request)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Greater(t, probe.remaining, 30*time.Second, "the ten-second admin timeout must not truncate a coordinated OAuth refresh")
}
