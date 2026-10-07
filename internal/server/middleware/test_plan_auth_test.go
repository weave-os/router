package middleware_test

import (
	"context"
	"errors"
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

func TestDirectTestAuthSkipsEmailSubscriptionsAndProviderSecrets(t *testing.T) {
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
	digest, persistent := policyregistry.ServingConversationDigest(subject, launch+"/"+session)
	scope := policyregistry.ServingAssertion{
		APIKeyID: key.ID, Scope: policyregistry.AdmissionScope{InstallationID: installation.ID, CredentialIdentity: subject, ConversationDigest: digest, Persistent: persistent},
		Admission: policyregistry.SessionReleaseBinding{Target: policyregistry.TargetStable, ActivationID: "exact-activation", BindingGeneration: 1},
		TestPlan:  &policyregistry.TestPlanScope{Plan: policyregistry.TestPlanStable, SubjectID: subject, SessionID: session, LaunchID: launch, PolicyRevision: strings.Repeat("a", 64), ExpiresAt: time.Now().Add(time.Hour)},
	}
	for _, scenario := range []string{"valid", "spoofed grant", "disabled budget", "wrong credential subject", "invalid credential"} {
		t.Run(scenario, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
			requestCredential := credential
			if scenario == "invalid credential" {
				requestCredential = "rk_invalid_test"
			}
			request.Header.Set(auth.RouterKeyHeader, requestCredential)
			request.Header.Set("X-Weave-User-Email", "customer@example.com")
			grant := "valid-grant"
			if scenario == "spoofed grant" {
				grant = "invalid-grant"
			}
			request.Header.Set(policyregistry.TestPlanGrantHeader, grant)
			request.Header.Set(policyregistry.TestPlanSessionHeader, session)
			key.CredentialSubjectID = subject
			if scenario == "wrong credential subject" {
				key.CredentialSubjectID = uuid.NewString()
			}
			engine := gin.New()
			engine.POST("/v1/messages", middleware.WithAuth(service, true, &middleware.ServingAdmissionConfig{Decisions: &policyregistry.AdmissionDecisionCache{}, Identity: policyregistry.WorkerIdentity{Target: policyregistry.TargetStable}, TestPlans: testGrantAdmitter{scope: scope}, TestBudgetEnabled: scenario != "disabled budget"}), func(c *gin.Context) {
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
			expected := map[string]int{"valid": http.StatusNoContent, "spoofed grant": http.StatusForbidden, "disabled budget": http.StatusForbidden, "wrong credential subject": http.StatusForbidden, "invalid credential": http.StatusUnauthorized}
			require.Equal(t, expected[scenario], response.Code)
		})
	}
}

type testGrantAdmitter struct {
	scope policyregistry.ServingAssertion
}

func (a testGrantAdmitter) Admit(_ context.Context, token, installation, key, session string) (policyregistry.ServingAssertion, error) {
	if token != "valid-grant" || installation != a.scope.Scope.InstallationID || key != a.scope.APIKeyID || session != a.scope.TestPlan.SessionID {
		return policyregistry.ServingAssertion{}, errors.New("invalid grant")
	}
	return a.scope, nil
}
