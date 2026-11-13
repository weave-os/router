package subscriptions_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

type keyRepo struct {
	auth.APIKeyRepository
	apiKey *auth.APIKey
}

func (r keyRepo) GetActiveByHashWithInstallation(context.Context, string) (*auth.APIKey, *auth.Installation, error) {
	apiKey := r.apiKey
	if apiKey == nil {
		apiKey = &auth.APIKey{ID: "key", CredentialSubjectID: "subject-sam", ExternalID: "kid-key", InstallationID: "installation", Scope: auth.ScopeRouting}
	}
	return apiKey, &auth.Installation{ID: "installation", ExternalID: "org-test"}, nil
}

func (keyRepo) MarkUsed(context.Context, string) (bool, error) { return false, nil }

type installationRepo struct{ auth.InstallationRepository }

func (installationRepo) MarkFirstRequestServed(context.Context, string) error { return nil }

type accountRepo struct {
	auth.SubscriptionAccountRepository
	candidates    []*auth.SubscriptionAccount
	candidatesErr error
	observedOwner *auth.SubscriptionOwner
}

func (accountRepo) ListSubscriptionAccounts(context.Context, auth.SubscriptionOwner) ([]*auth.SubscriptionAccount, error) {
	return nil, nil
}

func (r accountRepo) ListSubscriptionCandidates(_ context.Context, owner auth.SubscriptionOwner) ([]*auth.SubscriptionAccount, error) {
	if r.observedOwner != nil {
		*r.observedOwner = owner
	}
	return r.candidates, r.candidatesErr
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

func TestListPoolReturnsSharedCandidatesWithoutIdentifyingFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cooldownUntil := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	var observedOwner auth.SubscriptionOwner
	repo := accountRepo{
		candidates: []*auth.SubscriptionAccount{{
			Tier:                   auth.SubscriptionTierShared,
			ID:                     "private-account-id",
			Provider:               auth.SubscriptionProviderCodex,
			ExternalAccountID:      "private-external-account",
			ProviderUserID:         "private-provider-user",
			DisplayName:            "private display name",
			Enabled:                false,
			State:                  auth.SubscriptionAccountStateCooldown,
			CooldownUntil:          &cooldownUntil,
			RefreshTokenCiphertext: []byte("private credential"),
		}},
		observedOwner: &observedOwner,
	}
	apiKey := &auth.APIKey{ID: "shared-key", ExternalID: "kid-key", InstallationID: "installation", Scope: auth.ScopeRouting}
	engine := newPoolEngine(repo, apiKey)
	response := requestPool(engine)

	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var got []struct {
		Provider      auth.SubscriptionProvider     `json:"provider"`
		Tier          auth.SubscriptionTier         `json:"tier"`
		Enabled       bool                          `json:"enabled"`
		State         auth.SubscriptionAccountState `json:"state"`
		CooldownUntil *time.Time                    `json:"cooldown_until"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &got))
	require.Equal(t, []struct {
		Provider      auth.SubscriptionProvider     `json:"provider"`
		Tier          auth.SubscriptionTier         `json:"tier"`
		Enabled       bool                          `json:"enabled"`
		State         auth.SubscriptionAccountState `json:"state"`
		CooldownUntil *time.Time                    `json:"cooldown_until"`
	}{{
		Provider:      auth.SubscriptionProviderCodex,
		Tier:          auth.SubscriptionTierShared,
		Enabled:       false,
		State:         auth.SubscriptionAccountStateCooldown,
		CooldownUntil: &cooldownUntil,
	}}, got)
	require.Equal(t, auth.SubscriptionOwner{APIKeyID: "shared-key", InstallationID: "installation"}, observedOwner)

	var rawAccounts []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &rawAccounts))
	require.Len(t, rawAccounts, 1)
	for _, key := range []string{"id", "external_account_id", "display_name", "provider_user_id"} {
		_, exists := rawAccounts[0][key]
		require.False(t, exists, "response leaked %q", key)
	}
}

func TestListPoolReturnsEmptyArray(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := newPoolEngine(accountRepo{}, &auth.APIKey{ID: "key", InstallationID: "installation", Scope: auth.ScopeRouting})
	response := requestPool(engine)

	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.JSONEq(t, `[]`, response.Body.String())
}

func TestListPoolRequiresAPIKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	subscriptionsapi.Register(engine.Group("/v1"), nil)
	response := requestPool(engine)

	require.Equal(t, http.StatusUnauthorized, response.Code)
	require.JSONEq(t, `{"error":"router_key_required"}`, response.Body.String())
}

func TestListPoolReturnsUnavailableWhenCandidatesFail(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := newPoolEngine(accountRepo{candidatesErr: errors.New("candidate repository unavailable")}, &auth.APIKey{ID: "key", InstallationID: "installation", Scope: auth.ScopeRouting})
	response := requestPool(engine)

	require.Equal(t, http.StatusInternalServerError, response.Code)
	require.JSONEq(t, `{"error":"subscription_pool_unavailable"}`, response.Body.String())
}

func newPoolEngine(repo accountRepo, apiKey *auth.APIKey) *gin.Engine {
	svc := auth.NewService(installationRepo{}, keyRepo{apiKey: apiKey}, nil, nil, auth.NoOpAPIKeyCache{}, nil, time.Now).
		WithSubscriptionAccounts(repo)
	engine := gin.New()
	group := engine.Group("/v1", middleware.WithAuth(svc, false))
	subscriptionsapi.Register(group, svc)
	return engine
}

func requestPool(engine *gin.Engine) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "/v1/subscriptions/pool", nil)
	request.Header.Set("Authorization", "Bearer rk_test")
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	return response
}

func TestCreateAccountAttributesConnectionToInstallation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	events := &recorder{}
	now := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	svc := auth.NewService(installationRepo{}, keyRepo{}, nil, nil, auth.NoOpAPIKeyCache{}, nil, func() time.Time { return now }).
		WithSubscriptionAccounts(accountRepo{}).WithCodexEnrollmentVerifier(enrollmentVerifier{}).WithCredentialSubjectLookup(verifiedSubject{}).WithOnboardingObserver(events)
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

// Enrollment belongs to the verified personal key subject; an unsigned caller email cannot transfer ownership.
func TestCreateAccountEnrollsVerifiedPersonalKeyOwner(t *testing.T) {
	gin.SetMode(gin.TestMode)
	accounts := &ownerRecordingRepo{}
	svc := auth.NewService(installationRepo{}, keyRepo{}, nil, nil, auth.NoOpAPIKeyCache{}, nil, time.Now).
		WithSubscriptionAccounts(accounts).WithCodexEnrollmentVerifier(enrollmentVerifier{}).WithCredentialSubjectLookup(verifiedSubject{})
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
	request.Header.Set("X-Weave-User-Email", "Ali@Weave.test")
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)

	require.Equal(t, http.StatusCreated, response.Code, response.Body.String())
	require.Equal(t, auth.SubscriptionOwner{SubscriberID: "subject-sam", APIKeyID: "key", InstallationID: "installation"}, accounts.owner)
}

type verifiedSubject struct{}

// GetCredentialSubject admits only the key's own subject in its installation,
// so ownership cannot come from the caller email.
func (verifiedSubject) GetCredentialSubject(_ context.Context, subjectID, installationID string) (*auth.CredentialSubject, error) {
	if subjectID != "subject-sam" || installationID != "installation" {
		return nil, auth.ErrPersonalCredentialRequired
	}
	return &auth.CredentialSubject{ID: subjectID, ProjectionComplete: true, AccessEnabled: true}, nil
}

type enrollmentVerifier struct{}

func (enrollmentVerifier) VerifyCodexEnrollment(_ context.Context, _ string, refreshToken []byte) (auth.VerifiedCodexEnrollment, error) {
	return auth.VerifiedCodexEnrollment{ProviderUserID: "provider-user-1", RefreshToken: refreshToken}, nil
}
