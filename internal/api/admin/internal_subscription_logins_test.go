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
	"weave-os/router/internal/server/middleware"
	"weave-os/router/internal/subscriptions"
)

const otherSubscriberID = "9b6f3c4e-2f1a-4d8b-9c7e-5a4b3c2d1e0f"

type loginAccountRepo struct {
	internalSubscriptionAccountRepo
	enrolled          *auth.CreateSubscriptionAccountParams
	withoutRoutingKey bool
	upsertErr         error
}

const (
	testEnrollmentAPIKeyID       = "5d2c1b0a-9e8f-4a7b-8c6d-1e2f3a4b5c6d"
	testEnrollmentInstallationID = "7a6b5c4d-3e2f-4a1b-9c8d-0e1f2a3b4c5d"
)

func (r *loginAccountRepo) SubscriberEnrollmentOwner(_ context.Context, subscriberID string) (auth.SubscriptionOwner, error) {
	if r.withoutRoutingKey {
		return auth.SubscriptionOwner{}, auth.ErrSubscriptionEnrollmentKeyNotFound
	}
	return auth.SubscriptionOwner{SubscriberID: subscriberID, APIKeyID: testEnrollmentAPIKeyID, InstallationID: testEnrollmentInstallationID}, nil
}

func (r *loginAccountRepo) UpsertSubscriptionAccount(_ context.Context, params auth.CreateSubscriptionAccountParams) (*auth.SubscriptionAccount, auth.SubscriptionUpsertKind, error) {
	r.enrolled = &params
	if r.upsertErr != nil {
		return nil, auth.SubscriptionUpsertUpdated, r.upsertErr
	}
	return &auth.SubscriptionAccount{ID: testAccountID, Provider: params.Provider, ExternalAccountID: params.ExternalAccountID, Enabled: true, State: "unknown"}, auth.SubscriptionUpsertKind(""), nil
}

type fakeLoginFlow struct {
	returnPending      bool
	completionAttempts []subscriptions.PendingLogin
}

func (f *fakeLoginFlow) StartLogin(_ context.Context, provider subscriptions.Provider) (subscriptions.StartedLogin, error) {
	return subscriptions.StartedLogin{
		Pending:         subscriptions.PendingLogin{Provider: provider, Verifier: "secret-verifier", State: "state-1", ExpiresAt: time.Now().Add(time.Minute)},
		VerificationURL: "https://provider.example/authorize",
	}, nil
}

func (f *fakeLoginFlow) CompleteLogin(_ context.Context, pending subscriptions.PendingLogin, code string) (subscriptions.CompletedLogin, error) {
	f.completionAttempts = append(f.completionAttempts, pending)
	if f.returnPending {
		return subscriptions.CompletedLogin{}, subscriptions.ErrLoginPending
	}
	return subscriptions.CompletedLogin{Provider: pending.Provider, ExternalAccountID: "acct:org", DisplayName: "dev@example.com", RefreshToken: "rt-" + code}, nil
}

func loginTestEngine(t *testing.T, repo *loginAccountRepo, flow *fakeLoginFlow) *gin.Engine {
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
	group := engine.Group("/internal/v1", middleware.WithInternalServiceAuth(internalAccountsToken))
	group.POST("/subscription-logins/:subscriberID", admin.InternalStartSubscriptionLoginHandler(authSvc, flow))
	group.POST("/subscription-logins/:subscriberID/complete", admin.InternalCompleteSubscriptionLoginHandler(authSvc, flow))
	return engine
}

func postLogin(t *testing.T, engine *gin.Engine, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	encoded, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(encoded))
	req.Header.Set("X-Weave-Internal-Token", internalAccountsToken)
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)
	return recorder
}

func startLogin(t *testing.T, engine *gin.Engine) string {
	t.Helper()
	recorder := postLogin(t, engine, "/internal/v1/subscription-logins/"+testSubscriberID, map[string]string{"provider": "claude"})
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.NotContains(t, recorder.Body.String(), "secret-verifier", "PKCE secrets stay sealed")
	var started struct {
		LoginToken      string `json:"login_token"`
		VerificationURL string `json:"verification_url"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &started))
	require.Equal(t, "https://provider.example/authorize", started.VerificationURL)
	return started.LoginToken
}

func TestBrowserLoginEnrollsTheSubscriberThatStartedIt(t *testing.T) {
	repo := &loginAccountRepo{}
	flow := &fakeLoginFlow{}
	engine := loginTestEngine(t, repo, flow)
	token := startLogin(t, engine)

	recorder := postLogin(t, engine, "/internal/v1/subscription-logins/"+testSubscriberID+"/complete", map[string]string{"login_token": token, "code": "code#state-1"})
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
	require.NotNil(t, repo.enrolled)
	require.Equal(t, auth.SubscriptionOwner{SubscriberID: testSubscriberID, APIKeyID: testEnrollmentAPIKeyID, InstallationID: testEnrollmentInstallationID}, repo.enrolled.Owner)
	require.Equal(t, auth.SubscriptionProviderClaude, repo.enrolled.Provider)
	require.Equal(t, "acct:org", repo.enrolled.ExternalAccountID)
	require.NotEqual(t, []byte("rt-code#state-1"), repo.enrolled.RefreshToken, "refresh token is encrypted at rest")
	require.Equal(t, "secret-verifier", flow.completionAttempts[0].Verifier)
}

func TestBrowserLoginTokenCannotCompleteForAnotherSubscriber(t *testing.T) {
	repo := &loginAccountRepo{}
	flow := &fakeLoginFlow{}
	engine := loginTestEngine(t, repo, flow)
	token := startLogin(t, engine)

	recorder := postLogin(t, engine, "/internal/v1/subscription-logins/"+otherSubscriberID+"/complete", map[string]string{"login_token": token, "code": "code#state-1"})
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Empty(t, flow.completionAttempts)
	require.Nil(t, repo.enrolled)
}

func TestBrowserLoginReportsPendingUntilApproved(t *testing.T) {
	repo := &loginAccountRepo{}
	flow := &fakeLoginFlow{returnPending: true}
	engine := loginTestEngine(t, repo, flow)
	token := startLogin(t, engine)

	recorder := postLogin(t, engine, "/internal/v1/subscription-logins/"+testSubscriberID+"/complete", map[string]string{"login_token": token})
	require.Equal(t, http.StatusAccepted, recorder.Code)
	require.Nil(t, repo.enrolled)
}

func TestBrowserLoginWithoutRoutingKeyKeepsTheProviderCodeUnspent(t *testing.T) {
	repo := &loginAccountRepo{withoutRoutingKey: true}
	flow := &fakeLoginFlow{}
	engine := loginTestEngine(t, repo, flow)
	token := startLogin(t, engine)

	recorder := postLogin(t, engine, "/internal/v1/subscription-logins/"+testSubscriberID+"/complete", map[string]string{"login_token": token, "code": "code#state-1"})
	require.Equal(t, http.StatusConflict, recorder.Code, recorder.Body.String())
	require.Contains(t, recorder.Body.String(), "routing_key_required")
	require.Empty(t, flow.completionAttempts)
	require.Nil(t, repo.enrolled)
}

func TestBrowserLoginReportsAccountLinkedToAnotherOwner(t *testing.T) {
	repo := &loginAccountRepo{upsertErr: auth.ErrSubscriptionAccountHeldByAnotherOwner}
	flow := &fakeLoginFlow{}
	engine := loginTestEngine(t, repo, flow)
	token := startLogin(t, engine)

	recorder := postLogin(t, engine, "/internal/v1/subscription-logins/"+testSubscriberID+"/complete", map[string]string{"login_token": token, "code": "code#state-1"})
	require.Equal(t, http.StatusConflict, recorder.Code, recorder.Body.String())
	require.Contains(t, recorder.Body.String(), "subscription_account_linked_elsewhere")
}
