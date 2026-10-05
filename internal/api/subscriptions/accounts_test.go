package subscriptions_test

import (
	"bytes"
	"context"
	"database/sql"
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
	return &auth.APIKey{ID: "key", CredentialSubjectID: "subject-sam", ExternalID: "kid-key", InstallationID: "installation", Scope: auth.ScopeRouting},
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

func (*recorder) APIKeyFirstUsed(auth.APIKeyFirstUsedEvent)   {}
func (*recorder) HarnessLifecycle(auth.HarnessLifecycleEvent) {}
func (r *recorder) SubscriptionConnected(event auth.SubscriptionConnectedEvent) {
	r.events = append(r.events, event)
}

func TestCreateAccountAttributesConnectionToInstallation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	events := &recorder{}
	now := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	svc := auth.NewService(installationRepo{}, keyRepo{}, nil, nil, auth.NoOpAPIKeyCache{}, nil, func() time.Time { return now }).
		WithSubscriptionAccounts(accountRepo{}).WithCredentialSubjectLookup(verifiedSubject{}).WithOnboardingObserver(events)
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
		InstallationExternalID: "org-test", CredentialSubjectID: "subject-sam", APIKeyID: "key", AccountID: "account",
		Provider: auth.SubscriptionProviderCodex, OccurredAt: now,
	}}, events.events)
}

// ownerRecordingRepo captures who a connection was enrolled for.
type ownerRecordingRepo struct {
	auth.SubscriptionAccountRepository
	owner auth.SubscriptionOwner
}

func (r *ownerRecordingRepo) ListSubscriptionAccounts(context.Context, auth.SubscriptionOwner) ([]*auth.SubscriptionAccount, error) {
	return nil, nil
}

func (r *ownerRecordingRepo) UpsertSubscriptionAccount(_ context.Context, params auth.CreateSubscriptionAccountParams) (*auth.SubscriptionAccount, auth.SubscriptionUpsertKind, error) {
	r.owner = params.Owner
	return &auth.SubscriptionAccount{ID: "account", Provider: params.Provider}, auth.SubscriptionUpsertInserted, nil
}

// projectedIdentities is Weave's email-to-person projection for one installation.
type projectedIdentities map[string]string

func (p projectedIdentities) GetSubscriberForEmail(_ context.Context, _, email string) (string, error) {
	subjectID, projected := p[email]
	if !projected {
		return "", sql.ErrNoRows
	}
	return subjectID, nil
}

// Enrollment belongs to the verified personal key subject; an unsigned caller email cannot transfer ownership.
func TestCreateAccountEnrollsVerifiedPersonalKeyOwner(t *testing.T) {
	gin.SetMode(gin.TestMode)
	accounts := &ownerRecordingRepo{}
	svc := auth.NewService(installationRepo{}, keyRepo{}, nil, nil, auth.NoOpAPIKeyCache{}, nil, time.Now).
		WithSubscriptionAccounts(accounts).WithCredentialSubjectLookup(verifiedSubject{}).
		WithRequestIdentities(projectedIdentities{"sam@weave.test": "subject-sam"})
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
	request.Header.Set("X-Weave-User-Email", "Sam@Weave.test")
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)

	require.Equal(t, http.StatusCreated, response.Code, response.Body.String())
	require.Equal(t, auth.SubscriptionOwner{SubscriberID: "subject-sam", APIKeyID: "key", InstallationID: "installation"}, accounts.owner)
}

type verifiedSubject struct{}

func (verifiedSubject) GetCredentialSubject(context.Context, string, string) (*auth.CredentialSubject, error) {
	return &auth.CredentialSubject{ID: "subject-sam", ProjectionComplete: true, AccessEnabled: true}, nil
}
