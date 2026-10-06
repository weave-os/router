package middleware_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/server/middleware"
)

type forbiddenTestSecrets struct{ fakeExternalAPIKeyRepository }

func (forbiddenTestSecrets) GetForInstallation(context.Context, string) ([]*auth.ExternalAPIKey, error) {
	panic("test admission read provider secrets")
}

type forbiddenTestSubscriptions struct {
	failingSubscriptionAccountRepository
}

func (forbiddenTestSubscriptions) ListSubscriptionAccounts(context.Context, auth.SubscriptionOwner) ([]*auth.SubscriptionAccount, error) {
	panic("test admission read subscription accounts")
}

type assignedTestRoutingPolicyRepo struct{ policyReads, assignmentReads int }

func (r *assignedTestRoutingPolicyRepo) GetPolicy(context.Context, string) (auth.RoutingPolicy, error) {
	r.policyReads++
	return auth.RoutingPolicy{Mode: auth.RoutingPolicyAssigned, Revision: 1}, nil
}

func (r *assignedTestRoutingPolicyRepo) HasAssignment(context.Context, string, string, int64) (bool, error) {
	r.assignmentReads++
	return true, nil
}

func TestSignedTestAuthSkipsEmailSubscriptionsAndProviderSecrets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const credential = "rk_synthetic_test"
	subject, session, launch := uuid.NewString(), uuid.NewString(), uuid.NewString()
	key := &auth.APIKey{ID: "test-key", InstallationID: "test-installation", CredentialSubjectID: subject}
	installation := &auth.Installation{ID: key.InstallationID, ByokEnabled: true}
	repo := &fakeAPIKeyRepository{byHash: map[string]fakeKeyRow{auth.HashAPIKeySHA256(credential): {apiKey: key, installation: installation}}}
	routingPolicies := &assignedTestRoutingPolicyRepo{}
	service := auth.NewService(fakeInstallationRepository{}, repo, &forbiddenTestSecrets{}, nil, auth.NoOpAPIKeyCache{}, nil, time.Now).
		WithSubscriptionAccounts(forbiddenTestSubscriptions{}).
		WithRoutingPolicies(routingPolicies, nil)
	signer, err := policyregistry.NewAssertionSigner([]byte(strings.Repeat("s", 32)), time.Now)
	require.NoError(t, err)
	digest, persistent := policyregistry.ServingConversationDigest(subject, launch+"/"+session)
	scope := policyregistry.ServingAssertion{
		APIKeyID: key.ID, Scope: policyregistry.AdmissionScope{InstallationID: installation.ID, CredentialIdentity: subject, ConversationDigest: digest, Persistent: persistent},
		Admission: policyregistry.SessionReleaseBinding{Target: policyregistry.TargetStable, ActivationID: "exact-activation", BindingGeneration: 1},
		TestPlan:  &policyregistry.TestPlanScope{Plan: policyregistry.TestPlanStable, SubjectID: subject, SessionID: session, LaunchID: launch, PolicyRevision: strings.Repeat("a", 64), ExpiresAt: time.Now().Add(time.Hour)},
	}
	for _, scenario := range []string{"valid", "spoofed assertion", "disabled budget", "wrong credential subject"} {
		t.Run(scenario, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/probe", strings.NewReader(`{}`))
			request.Header.Set(auth.RouterKeyHeader, credential)
			request.Header.Set("X-Weave-User-Email", "customer@example.com")
			encoded, err := signer.Sign(scope, request, []byte(`{}`), credential)
			require.NoError(t, err)
			if scenario == "spoofed assertion" {
				encoded += "x"
			}
			request.Header.Set(policyregistry.ServingAssertionHeader, encoded)
			key.CredentialSubjectID = subject
			if scenario == "wrong credential subject" {
				key.CredentialSubjectID = uuid.NewString()
			}
			engine := gin.New()
			engine.POST("/probe", middleware.WithAuth(service, true, &middleware.ServingAdmissionConfig{Signer: signer, TestBudgetEnabled: scenario != "disabled budget"}), func(c *gin.Context) {
				require.Empty(t, middleware.SubscriptionOwnerFrom(c).SubscriberID)
				require.False(t, auth.RoutingPassthroughFrom(c.Request.Context()), "signed test launches force Router plan routing")
				require.Nil(t, c.Request.Context().Value(proxy.ExternalAPIKeysContextKey{}))
				identity := requestcontext.ClientIdentity{Email: "customer@example.com", AccountID: "spoofed", SessionID: "old-session"}
				ctx := requestcontext.WithClientIdentity(c.Request.Context(), identity)
				require.Equal(t, subject, requestcontext.ClientIdentityFrom(ctx).AccountID)
				require.Equal(t, session, requestcontext.ClientIdentityFrom(ctx).SessionID)
				require.Empty(t, requestcontext.ClientIdentityFrom(ctx).Email)
				c.Status(http.StatusNoContent)
			})
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, request)
			require.Zero(t, routingPolicies.policyReads)
			require.Zero(t, routingPolicies.assignmentReads)
			expected := map[string]int{"valid": http.StatusNoContent, "spoofed assertion": http.StatusUnauthorized, "disabled budget": http.StatusServiceUnavailable, "wrong credential subject": http.StatusForbidden}
			require.Equal(t, expected[scenario], response.Code)
		})
	}
}
