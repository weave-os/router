package subscriptions_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	subscriptionsapi "weave-os/router/internal/api/subscriptions"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/server/middleware"
)

type keyRepo struct{ auth.APIKeyRepository }

func (keyRepo) GetActiveByHashWithInstallation(context.Context, string) (*auth.APIKey, *auth.Installation, error) {
	return &auth.APIKey{ID: "key", ExternalID: "kid-key", InstallationID: "installation", Scope: auth.ScopeRouting},
		&auth.Installation{ID: "installation", ExternalID: "org-test"}, nil
}

func (keyRepo) MarkUsed(context.Context, string) (bool, error) { return false, nil }

type installationRepo struct{ auth.InstallationRepository }

func (installationRepo) MarkFirstRequestServed(context.Context, string) error { return nil }

type accountRepo struct {
	auth.SubscriptionAccountRepository
}

func (accountRepo) ListSubscriptionAccounts(context.Context, auth.SubscriptionOwner) ([]*auth.SubscriptionAccount, error) {
	return nil, nil
}

func (accountRepo) UpsertSubscriptionAccount(_ context.Context, params auth.CreateSubscriptionAccountParams) (*auth.SubscriptionAccount, auth.SubscriptionUpsertKind, error) {
	return &auth.SubscriptionAccount{ID: "account", Provider: params.Provider}, auth.SubscriptionUpsertInserted, nil
}

type recorder struct {
	events []auth.SubscriptionConnectedEvent
}

func (*recorder) APIKeyFirstUsed(auth.APIKeyFirstUsedEvent) {}
func (r *recorder) SubscriptionConnected(event auth.SubscriptionConnectedEvent) {
	r.events = append(r.events, event)
}

func TestCreateAccountAttributesConnectionToInstallation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	events := &recorder{}
	now := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	svc := auth.NewService(installationRepo{}, keyRepo{}, nil, nil, auth.NoOpAPIKeyCache{}, nil, func() time.Time { return now }).
		WithSubscriptionAccounts(accountRepo{}).WithOnboardingObserver(events)
	engine := gin.New()
	group := engine.Group("/v1", middleware.WithAuth(svc, false))
	subscriptionsapi.Register(group, svc)
	body, err := json.Marshal(gin.H{
		"provider": auth.SubscriptionProviderCodex, "external_account_id": "external-account", "refresh_token": "refresh",
	})
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodPost, "/v1/subscriptions/accounts", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer rk_test")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	require.Equal(t, http.StatusCreated, response.Code, response.Body.String())
	require.Equal(t, []auth.SubscriptionConnectedEvent{{
		InstallationExternalID: "org-test", APIKeyID: "key", AccountID: "account",
		Provider: auth.SubscriptionProviderCodex, OccurredAt: now,
	}}, events.events)
}
