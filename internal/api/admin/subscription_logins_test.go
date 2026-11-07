package admin_test

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
	"github.com/tink-crypto/tink-go/v2/aead"
	"github.com/tink-crypto/tink-go/v2/insecurecleartextkeyset"
	"github.com/tink-crypto/tink-go/v2/keyset"

	"weave-os/router/internal/api/admin"
	"weave-os/router/internal/auth"
)

type adminLoginAccountRepo struct {
	loginAccountRepo
	owners []auth.SubscriptionEnrollmentCandidate
	seats  []auth.SubscriptionSeat
}

func (r *adminLoginAccountRepo) ListSubscriptionSeats(context.Context) ([]auth.SubscriptionSeat, error) {
	return r.seats, nil
}

func (r *adminLoginAccountRepo) ListSubscriptionEnrollmentOwners(context.Context) ([]auth.SubscriptionEnrollmentCandidate, error) {
	return r.owners, nil
}

func adminLoginTestEngine(t *testing.T, repo *adminLoginAccountRepo, flow *fakeLoginFlow) *gin.Engine {
	t.Helper()
	handle, err := keyset.NewHandle(aead.AES256GCMKeyTemplate())
	require.NoError(t, err)
	var buf bytes.Buffer
	require.NoError(t, insecurecleartextkeyset.Write(handle, keyset.NewJSONWriter(&buf)))
	encryptor, err := auth.NewTinkEncryptor(buf.String())
	require.NoError(t, err)
	gin.SetMode(gin.TestMode)
	authSvc := auth.NewService(nil, nil, nil, nil, auth.NoOpAPIKeyCache{}, nil, time.Now).
		WithSubscriptionAccounts(repo).WithEncryptor(encryptor)
	engine := gin.New()
	group := engine.Group("/admin/v1")
	group.GET("/subscription-owners", admin.ListSubscriptionOwnersHandler(authSvc, true))
	group.GET("/subscription-accounts", admin.ListSubscriptionSeatsHandler(authSvc))
	group.DELETE("/subscription-accounts/:subscriberID/:accountID", admin.InternalDeleteSubscriptionAccountHandler(authSvc))
	group.POST("/subscription-logins/:subscriberID", admin.AdminStartSubscriptionLoginHandler(authSvc, flow))
	group.POST("/subscription-logins/:subscriberID/complete", admin.AdminCompleteSubscriptionLoginHandler(authSvc, flow))
	return engine
}

func adminPost(t *testing.T, engine *gin.Engine, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	encoded, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(encoded))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)
	return recorder
}

func adminStartLogin(t *testing.T, engine *gin.Engine, subscriberID string) string {
	t.Helper()
	recorder := adminPost(t, engine, "/admin/v1/subscription-logins/"+subscriberID, map[string]string{"provider": "claude"})
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var started struct {
		LoginToken string `json:"login_token"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &started))
	return started.LoginToken
}

func TestAdminLoginEnrollsThePickedSubscriberThroughTheirRoutingKey(t *testing.T) {
	repo := &adminLoginAccountRepo{}
	engine := adminLoginTestEngine(t, repo, &fakeLoginFlow{})
	token := adminStartLogin(t, engine, testSubscriberID)

	recorder := adminPost(t, engine, "/admin/v1/subscription-logins/"+testSubscriberID+"/complete", map[string]string{"login_token": token, "code": "code#state-1"})
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
	require.NotNil(t, repo.enrolled)
	require.Equal(t, auth.SubscriptionOwner{SubscriberID: testSubscriberID, APIKeyID: testEnrollmentAPIKeyID, InstallationID: testEnrollmentInstallationID}, repo.enrolled.Owner)
	require.Equal(t, "acct:org", repo.enrolled.ExternalAccountID)
}

func TestAdminLoginRefusesSubscriberWithoutRoutingKeyBeforeContactingProvider(t *testing.T) {
	repo := &adminLoginAccountRepo{loginAccountRepo: loginAccountRepo{withoutRoutingKey: true}}
	engine := adminLoginTestEngine(t, repo, &fakeLoginFlow{})

	recorder := adminPost(t, engine, "/admin/v1/subscription-logins/"+testSubscriberID, map[string]string{"provider": "claude"})
	require.Equal(t, http.StatusConflict, recorder.Code, recorder.Body.String())
	require.Contains(t, recorder.Body.String(), "routing_key_required")
}

func TestAdminLoginTokenOnlyCompletesForTheSubscriberItWasStartedFor(t *testing.T) {
	repo := &adminLoginAccountRepo{}
	flow := &fakeLoginFlow{}
	engine := adminLoginTestEngine(t, repo, flow)
	token := adminStartLogin(t, engine, testSubscriberID)

	recorder := adminPost(t, engine, "/admin/v1/subscription-logins/"+otherSubscriberID+"/complete", map[string]string{"login_token": token, "code": "code#state-1"})
	require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
	require.Empty(t, flow.completionAttempts)
	require.Nil(t, repo.enrolled)
}

func TestAdminSubscriptionOwnersListsEnrollableSubjects(t *testing.T) {
	repo := &adminLoginAccountRepo{owners: []auth.SubscriptionEnrollmentCandidate{{SubscriberID: testSubscriberID, DisplayName: "Ada", Email: "ada@example.com", KeyPrefix: "rk_abcde", KeySuffix: "wxyz"}}}
	engine := adminLoginTestEngine(t, repo, &fakeLoginFlow{})

	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin/v1/subscription-owners", nil))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.JSONEq(t, `{"enabled":true,"owners":[{"subscriber_id":"`+testSubscriberID+`","display_name":"Ada","email":"ada@example.com","key_prefix":"rk_abcde","key_suffix":"wxyz"}]}`, recorder.Body.String())
}

func TestAdminSubscriptionOwnersReportsDisabledPools(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/admin/v1/subscription-owners", admin.ListSubscriptionOwnersHandler(nil, false))

	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin/v1/subscription-owners", nil))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.JSONEq(t, `{"enabled":false,"owners":[]}`, recorder.Body.String())
}

func TestAdminCanUnlinkSeatWhoseOwnerHasNoRoutingKey(t *testing.T) {
	const accountID = "6f1c2c7e-3c35-4f4f-9d43-1d0a3d3e7c11"
	repo := &adminLoginAccountRepo{seats: []auth.SubscriptionSeat{{
		AccountID: accountID, SubscriberID: testSubscriberID, Provider: auth.SubscriptionProviderClaude,
		DisplayName: "ada@claude", Enabled: true, OwnerDisplayName: "Ada", OwnerEmail: "ada@example.com",
	}}}
	engine := adminLoginTestEngine(t, repo, &fakeLoginFlow{})

	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin/v1/subscription-accounts", nil))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.JSONEq(t, `{"seats":[{"account_id":"`+accountID+`","subscriber_id":"`+testSubscriberID+`","provider":"claude","display_name":"ada@claude","enabled":true,"owner_display_name":"Ada","owner_email":"ada@example.com","owner_has_routing_key":false}]}`, recorder.Body.String())

	recorder = httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodDelete, "/admin/v1/subscription-accounts/"+testSubscriberID+"/"+accountID, nil))
	require.Equal(t, http.StatusNoContent, recorder.Code, recorder.Body.String())
	require.Equal(t, accountID, repo.deletedID)
	require.Equal(t, auth.SubscriptionOwner{SubscriberID: testSubscriberID}, repo.deletedOwner)
}
