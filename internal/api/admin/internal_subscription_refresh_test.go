package admin_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/api/admin"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/server/middleware"
	"weave-os/router/internal/subscriptions"
)

type accountRefreshStub struct {
	calls int
	owner auth.SubscriptionOwner
	id    string
	err   error
}

func (s *accountRefreshStub) RefreshAccount(_ context.Context, owner auth.SubscriptionOwner, id string) (subscriptions.AccountRefreshResult, error) {
	s.calls++
	s.owner, s.id = owner, id
	return subscriptions.AccountRefreshResult{AccountID: id, Provider: subscriptions.ProviderCodex,
		TokenRefreshVersion: 3, NextRefreshAt: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}, s.err
}

func TestInternalSubscriptionRefreshAuthenticatesAndScopesSeat(t *testing.T) {
	const subscriber = "11111111-1111-1111-1111-111111111111"
	const seat = "22222222-2222-2222-2222-222222222222"
	for _, scenario := range []struct {
		name          string
		authenticated bool
		err           error
		status        int
	}{
		{name: "unauthenticated", status: http.StatusUnauthorized},
		{name: "refresh", authenticated: true, status: http.StatusOK},
		{name: "foreign seat", authenticated: true, err: auth.ErrSubscriptionAccountNotFound, status: http.StatusNotFound},
		{name: "disabled seat", authenticated: true, err: subscriptions.ErrNoAvailableAccount, status: http.StatusConflict},
		{name: "issuer failure", authenticated: true, err: errors.New("synthetic issuer failure"), status: http.StatusBadGateway},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			stub := &accountRefreshStub{err: scenario.err}
			engine := gin.New()
			group := engine.Group("/internal/v1", middleware.WithInternalServiceAuth(internalAccountsToken))
			group.POST("/subscription-accounts/:subscriberID/:accountID/refresh", admin.InternalRefreshSubscriptionAccountHandler(stub))
			request := httptest.NewRequest(http.MethodPost, "/internal/v1/subscription-accounts/"+subscriber+"/"+seat+"/refresh", nil)
			if scenario.authenticated {
				request.Header.Set("X-Weave-Internal-Token", internalAccountsToken)
			}
			rec := httptest.NewRecorder()
			engine.ServeHTTP(rec, request)
			require.Equal(t, scenario.status, rec.Code)
			if !scenario.authenticated {
				require.Zero(t, stub.calls)
				return
			}
			require.Equal(t, subscriber, stub.owner.SubscriberID)
			require.Equal(t, seat, stub.id)
			var response map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
			require.NotContains(t, response, "access_token")
			require.NotContains(t, response, "refresh_token")
		})
	}
}
