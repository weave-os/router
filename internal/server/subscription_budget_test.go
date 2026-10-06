package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/server"
	"weave-os/router/internal/subscriptions"
)

type subscriptionBudgetKeys struct{ auth.APIKeyRepository }

func (subscriptionBudgetKeys) GetActiveByHashWithInstallation(context.Context, string) (*auth.APIKey, *auth.Installation, error) {
	return &auth.APIKey{ID: "key", InstallationID: "installation", CredentialSubjectID: "subject", Scope: auth.ScopeRouting}, &auth.Installation{ID: "installation", ExternalID: "synthetic-installation"}, nil
}
func (subscriptionBudgetKeys) MarkUsed(context.Context, string) (bool, error) { return false, nil }

type subscriptionBudgetOwners struct{}

func (subscriptionBudgetOwners) GetCredentialSubject(context.Context, string, string) (*auth.CredentialSubject, error) {
	return &auth.CredentialSubject{ID: "subject", ProjectionComplete: true, AccessEnabled: true}, nil
}

type subscriptionBudgetAccounts struct {
	auth.SubscriptionAccountRepository
}

func (subscriptionBudgetAccounts) UpsertSubscriptionAccount(_ context.Context, params auth.CreateSubscriptionAccountParams) (*auth.SubscriptionAccount, auth.SubscriptionUpsertKind, error) {
	return &auth.SubscriptionAccount{ID: "account", Provider: params.Provider, ProviderUserID: params.ProviderUserID}, auth.SubscriptionUpsertInserted, nil
}

type subscriptionBudgetVerifier struct{}

func (subscriptionBudgetVerifier) VerifyCodexEnrollment(ctx context.Context, _ string, refreshToken []byte) (auth.VerifiedCodexEnrollment, error) {
	// Model an exchange that needs its entire documented budget without a slow
	// sleep: reject a route whose remaining deadline cannot accommodate it.
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) <= subscriptions.RefreshHTTPTimeout {
		return auth.VerifiedCodexEnrollment{}, context.DeadlineExceeded
	}
	return auth.VerifiedCodexEnrollment{ProviderUserID: "provider-user", RefreshToken: refreshToken}, nil
}

func TestRegisteredSubscriptionRouteAllowsProviderExchangeBudget(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := auth.NewService(nil, subscriptionBudgetKeys{}, nil, nil, auth.NoOpAPIKeyCache{}, nil, time.Now).WithSubscriptionAccounts(subscriptionBudgetAccounts{}).WithCredentialSubjectLookup(subscriptionBudgetOwners{}).WithCodexEnrollmentVerifier(subscriptionBudgetVerifier{})
	engine := gin.New()
	server.Register(engine, svc, nil, nil, nil, server.DeploymentModeManaged, nil, nil, nil, nil)
	request := httptest.NewRequest(http.MethodPost, "/v1/subscriptions/accounts", strings.NewReader(`{"provider":"codex","external_account_id":"workspace","refresh_token":"synthetic-refresh"}`))
	request.Header.Set("Authorization", "Bearer rk_synthetic")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	require.Equal(t, http.StatusCreated, response.Code, response.Body.String())
	require.Contains(t, response.Body.String(), `"id":"account"`)
}
