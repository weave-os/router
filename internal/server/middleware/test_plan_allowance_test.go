package middleware

import (
	"context"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/subscriptions/entitlement"
)

func TestVerifiedInternalTestSkipsAllSubscriberAllowanceAndProductReads(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set(ctxKeyAPIKey, &auth.APIKey{ID: "test-key", CredentialSubjectID: "real-subscriber"})
		c.Set(ctxKeySubscriptionOwner, auth.SubscriptionOwner{SubscriberID: "real-subscriber"})
		ctx := requestcontext.WithInternalTestIdentity(context.Background(), requestcontext.InternalTestIdentity{SubjectID: "test-subject", SessionID: "fresh-session"})
		c.Request = c.Request.WithContext(entitlement.WithProductScope(ctx, entitlement.PlanMax))
	})
	// A nil service panics if either subscriber path is entered.
	engine.Use(WithSubscriberAllowance(nil), WithSubscriberProductScope(nil))
	engine.POST("/v1/messages", func(c *gin.Context) {
		require.Empty(t, SubscriptionOwnerFrom(c).SubscriberID)
		plan, ok := entitlement.ProductScopeFromContext(c.Request.Context())
		require.True(t, ok)
		require.Equal(t, entitlement.PlanMax, plan)
		_, covered := entitlement.CoverageFromContext(c.Request.Context())
		require.False(t, covered)
		c.Status(http.StatusNoContent)
	})
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	request.Header.Set("X-Weave-User-Email", "customer@example.com")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusNoContent, recorder.Code)
}

func TestWorkerLoadsExactTestProfileWithoutSubscriberEntitlement(t *testing.T) {
	for _, plan := range []policyregistry.TestPlan{policyregistry.TestPlanStable, policyregistry.TestPlanMax, policyregistry.TestPlanBoost} {
		t.Run(string(plan), func(t *testing.T) {
			cfg, admitted, store, _ := admissionMiddlewareFixture(t)
			cfg.TestBudgetEnabled = true
			var release policyregistry.ServingRelease
			require.NoError(t, json.Unmarshal(store.objects[admitted.Admission.Selection.Release], &release))
			profile, err := plan.Profile()
			require.NoError(t, err)
			if profile.Key != "" {
				ref := store.put(t, policyregistry.ServingProfiles, &policyregistry.RoutingProfile{SchemaVersion: policyregistry.ServingProfileV1, ProfileKey: profile.Key, Policy: release.Policy, Requirements: release.Requirements})
				admitted.Admission.ProfileKey = profile.Key
				admitted.Admission.Selection.Profile = &ref
			}
			subject, launch, session := uuid.NewString(), uuid.NewString(), uuid.NewString()
			admitted.Scope.CredentialIdentity = subject
			admitted.Scope.ConversationDigest, admitted.Scope.Persistent = policyregistry.ServingConversationDigest(subject, launch+"/"+session)
			admitted.TestPlan = &policyregistry.TestPlanScope{Plan: plan, SubjectID: subject, LaunchID: launch, SessionID: session, PolicyRevision: release.Policy.SHA256, ExpiresAt: admitted.Admission.LastAdmittedAt.Add(time.Hour)}
			cfg.Attribution = admissionAttributionFunc(func(_ context.Context, _ string, assertion policyregistry.ServingAssertion) error {
				require.Equal(t, subject, assertion.TestPlan.SubjectID)
				require.Equal(t, admitted.Admission.Selection, assertion.Admission.Selection)
				return nil
			})
			response := runAdmissionMiddleware(t, cfg, admitted, func(c *gin.Context) {
				require.Empty(t, SubscriptionOwnerFrom(c).SubscriberID)
				actual, exists := entitlement.ProductScopeFromContext(c.Request.Context())
				require.Equal(t, plan != policyregistry.TestPlanStable, exists)
				if exists {
					require.Equal(t, entitlement.Plan(plan), actual)
				}
				snapshot := policyregistry.ServingSnapshotFromContext(c.Request.Context())
				require.Equal(t, release.Policy.SHA256, snapshot.Release.Policy.SHA256)
				c.Status(http.StatusNoContent)
			})
			require.Equal(t, http.StatusNoContent, response.Code)
			admitted.TestPlan.PolicyRevision = policyregistry.Digest([]byte("different-policy"))
			response = runAdmissionMiddleware(t, cfg, admitted, func(*gin.Context) { t.Fatal("mismatched test policy reached dispatch") })
			require.Equal(t, http.StatusForbidden, response.Code)
		})
	}
}
