package admin_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"weave-os/router/internal/api/admin"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/server/middleware"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	internalAccountsToken = "internal-accounts-token"
	testSubscriberID      = "2e474d5e-e8f8-4e71-af19-1ccb88cc8db5"
	testAccountID         = "0d8b37a9-37d8-4507-b414-f12938d7e0af"
)

type internalSubscriptionAccountRepo struct {
	accounts     []*auth.SubscriptionAccount
	updatedOwner auth.SubscriptionOwner
	updatedID    string
	enabled      bool
	deletedOwner auth.SubscriptionOwner
	deletedID    string
	mutationErr  error
}

func (r *internalSubscriptionAccountRepo) UpsertSubscriptionAccount(context.Context, auth.CreateSubscriptionAccountParams) (*auth.SubscriptionAccount, error) {
	panic("not used")
}

func (r *internalSubscriptionAccountRepo) ListSubscriptionAccounts(_ context.Context, owner auth.SubscriptionOwner) ([]*auth.SubscriptionAccount, error) {
	if owner.SubscriberID != testSubscriberID {
		return nil, auth.ErrSubscriptionAccountNotFound
	}
	return r.accounts, nil
}

func (r *internalSubscriptionAccountRepo) UpdateSubscriptionAccountState(_ context.Context, accountID string, owner auth.SubscriptionOwner, enabled bool, _ *time.Time) error {
	r.updatedOwner = owner
	r.updatedID = accountID
	r.enabled = enabled
	return r.mutationErr
}

func (r *internalSubscriptionAccountRepo) UpdateSubscriptionAccountCooldown(context.Context, string, auth.SubscriptionOwner, time.Time) error {
	panic("not used")
}

func (r *internalSubscriptionAccountRepo) UpdateSubscriptionRefreshToken(context.Context, string, auth.SubscriptionOwner, []byte) error {
	panic("not used")
}

func (r *internalSubscriptionAccountRepo) DeleteSubscriptionAccount(_ context.Context, accountID string, owner auth.SubscriptionOwner) error {
	r.deletedOwner = owner
	r.deletedID = accountID
	return r.mutationErr
}

func (r *internalSubscriptionAccountRepo) TryAcquireSubscriptionRefreshLease(context.Context, string, auth.SubscriptionOwner, string, time.Duration) (auth.RefreshLeaseAcquisition, error) {
	panic("not used")
}

func (r *internalSubscriptionAccountRepo) ExtendSubscriptionRefreshLease(context.Context, string, auth.SubscriptionOwner, string, time.Duration) (int64, error) {
	panic("not used")
}

func (r *internalSubscriptionAccountRepo) ReleaseSubscriptionRefreshLease(context.Context, string, auth.SubscriptionOwner, string) error {
	panic("not used")
}

func (r *internalSubscriptionAccountRepo) DisableSubscriptionAccountIfRefreshHolder(context.Context, string, auth.SubscriptionOwner, string, int64) error {
	panic("not used")
}

func (r *internalSubscriptionAccountRepo) CooldownSubscriptionAccountIfRefreshHolder(context.Context, string, auth.SubscriptionOwner, string, int64, time.Time) error {
	panic("not used")
}

func (r *internalSubscriptionAccountRepo) GetSubscriptionCredentialRecord(context.Context, string, auth.SubscriptionOwner) (*auth.SubscriptionCredentialRecord, error) {
	panic("not used")
}

func (r *internalSubscriptionAccountRepo) PersistSubscriptionTokens(context.Context, string, auth.SubscriptionOwner, string, int64, []byte, []byte, time.Time) error {
	panic("not used")
}

func internalSubscriptionAccountsEngine(repo *internalSubscriptionAccountRepo) *gin.Engine {
	gin.SetMode(gin.TestMode)
	authSvc := auth.NewService(nil, nil, nil, nil, auth.NoOpAPIKeyCache{}, nil, time.Now).
		WithSubscriptionAccounts(repo)
	engine := gin.New()
	group := engine.Group("/internal/v1", middleware.WithInternalServiceAuth(internalAccountsToken))
	group.GET("/subscription-accounts/:subscriberID", admin.InternalListSubscriptionAccountsHandler(authSvc))
	group.PATCH("/subscription-accounts/:subscriberID/:accountID", admin.InternalUpdateSubscriptionAccountHandler(authSvc))
	group.DELETE("/subscription-accounts/:subscriberID/:accountID", admin.InternalDeleteSubscriptionAccountHandler(authSvc))
	return engine
}

func internalSubscriptionAccountsRequest(method, path string, body []byte) *http.Request {
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	request.Header.Set("X-Weave-Internal-Token", internalAccountsToken)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	return request
}

func TestInternalSubscriptionAccountsListsSafeHealthMetadata(t *testing.T) {
	resetAt := time.Date(2026, time.September, 20, 12, 0, 0, 0, time.UTC)
	repo := &internalSubscriptionAccountRepo{accounts: []*auth.SubscriptionAccount{{
		ID: testAccountID, Provider: auth.SubscriptionProviderClaude, ExternalAccountID: "claude@example.com", DisplayName: "Claude account",
		Enabled: true, State: auth.SubscriptionAccountStateExhausted, CooldownUntil: &resetAt,
		RefreshTokenCiphertext: []byte("never-returned"),
	}}}
	engine := internalSubscriptionAccountsEngine(repo)
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, internalSubscriptionAccountsRequest(
		http.MethodGet,
		"/internal/v1/subscription-accounts/"+testSubscriberID,
		nil,
	))

	require.Equal(t, http.StatusOK, response.Code)
	var body []map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.Len(t, body, 1)
	assert.Equal(t, "claude", body[0]["provider"])
	assert.Equal(t, "exhausted", body[0]["state"])
	assert.Equal(t, "claude@example.com", body[0]["external_account_id"])
	assert.Equal(t, "Claude account", body[0]["display_name"])
	assert.NotContains(t, response.Body.String(), "never-returned")
}

func TestInternalSubscriptionAccountsMutationsStaySubscriberScoped(t *testing.T) {
	repo := &internalSubscriptionAccountRepo{}
	engine := internalSubscriptionAccountsEngine(repo)

	updateResponse := httptest.NewRecorder()
	engine.ServeHTTP(updateResponse, internalSubscriptionAccountsRequest(
		http.MethodPatch,
		"/internal/v1/subscription-accounts/"+testSubscriberID+"/"+testAccountID,
		[]byte(`{"enabled":false}`),
	))
	require.Equal(t, http.StatusNoContent, updateResponse.Code)
	assert.Equal(t, auth.SubscriptionOwner{SubscriberID: testSubscriberID}, repo.updatedOwner)
	assert.Equal(t, testAccountID, repo.updatedID)
	assert.False(t, repo.enabled)

	deleteResponse := httptest.NewRecorder()
	engine.ServeHTTP(deleteResponse, internalSubscriptionAccountsRequest(
		http.MethodDelete,
		"/internal/v1/subscription-accounts/"+testSubscriberID+"/"+testAccountID,
		nil,
	))
	require.Equal(t, http.StatusNoContent, deleteResponse.Code)
	assert.Equal(t, auth.SubscriptionOwner{SubscriberID: testSubscriberID}, repo.deletedOwner)
	assert.Equal(t, testAccountID, repo.deletedID)
}

func TestInternalSubscriptionAccountsRejectInvalidOwnershipAndRequireServiceToken(t *testing.T) {
	engine := internalSubscriptionAccountsEngine(&internalSubscriptionAccountRepo{})

	invalidResponse := httptest.NewRecorder()
	engine.ServeHTTP(invalidResponse, internalSubscriptionAccountsRequest(
		http.MethodGet,
		"/internal/v1/subscription-accounts/not-a-subscriber",
		nil,
	))
	assert.Equal(t, http.StatusBadRequest, invalidResponse.Code)

	unauthorizedResponse := httptest.NewRecorder()
	engine.ServeHTTP(unauthorizedResponse, httptest.NewRequest(
		http.MethodGet,
		"/internal/v1/subscription-accounts/"+testSubscriberID,
		nil,
	))
	assert.Equal(t, http.StatusUnauthorized, unauthorizedResponse.Code)
}
