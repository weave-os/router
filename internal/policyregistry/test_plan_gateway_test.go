package policyregistry_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/gateway"
	"weave-os/router/internal/policyregistry"
)

type planGatewayCredential struct{ installation string }

func (c planGatewayCredential) VerifyRoutingCredential(context.Context, string) (*auth.Installation, *auth.APIKey, error) {
	return &auth.Installation{ID: c.installation}, &auth.APIKey{ID: "test-key"}, nil
}

type forbiddenOrdinaryAdmission struct{ calls int }

func (s *forbiddenOrdinaryAdmission) Admit(context.Context, string, string, string, policyregistry.AdmissionDecision) (policyregistry.AdmissionScope, policyregistry.SessionReleaseBinding, error) {
	s.calls++
	return policyregistry.AdmissionScope{}, policyregistry.SessionReleaseBinding{}, errors.New("ordinary admission must not run")
}

type planGatewayIAM struct{}

func (planGatewayIAM) IdentityToken(context.Context, string) (string, error) {
	return "synthetic-iam", nil
}

func TestGatewayTestLaunchSignsPinnedStableAndStripsClientAuthority(t *testing.T) {
	tools, repo, store, now, original := testPlanFixture(t)
	signer, err := policyregistry.NewAssertionSigner([]byte(strings.Repeat("s", 32)), func() time.Time { return *now })
	require.NoError(t, err)
	type observed struct {
		assertion policyregistry.ServingAssertion
		headers   http.Header
		err       error
	}
	observations := make(chan observed, 1)
	worker := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		assertion, verifyErr := signer.Verify(r.Header.Get(policyregistry.ServingAssertionHeader), r, body, "rk_synthetic")
		observations <- observed{assertion, r.Header.Clone(), verifyErr}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer worker.Close()
	binding := store.object(t, policyregistry.ServingBindings, original.Default.Binding).(*policyregistry.DeploymentBinding)
	revision := binding.Router
	revision.URL = worker.URL
	original.Default = publishSelection(t, store, original.Default, *store.object(t, policyregistry.ServingReleases, original.Default.Release).(*policyregistry.ServingRelease), revision, binding.Classifier, nil)
	// Stable tests use the default profile; publishing only it prevents accidental profile fallback.
	original.Profiles = map[string]policyregistry.ServingSelection{}
	state, _ := storedActivateFixture(t, store, policyregistry.ServingStateSnapshot{}, original, servingEpoch)
	store.states[policyregistry.TargetStable] = state
	preview, err := tools.Preview(context.Background(), repo.identity.SubjectID, policyregistry.TestPlanStable)
	require.NoError(t, err)
	config, err := tools.Prepare(context.Background(), preview, true)
	require.NoError(t, err)
	ordinary := &forbiddenOrdinaryAdmission{}
	handler, err := gateway.NewHandler(planGatewayCredential{repo.identity.InstallationID}, ordinary, store, signer, planGatewayIAM{}, worker.Client().Transport)
	require.NoError(t, err)
	handler.WithTestPlans(tools)
	session := uuid.NewString()
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"auto","metadata":{"user_id":"spoofed-customer"}}`))
	request.Header.Set(auth.RouterKeyHeader, "rk_synthetic")
	request.Header.Set("Authorization", "Bearer customer-subscription")
	request.Header.Set("x-api-key", "customer-provider-key")
	request.Header.Set("X-Weave-User-Email", "customer@example.com")
	request.Header.Set("X-Weave-Serving-Target", "prod/weave-internal")
	request.Header.Set(policyregistry.TestPlanGrantHeader, config.Grant)
	request.Header.Set(policyregistry.TestPlanSessionHeader, session)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusNoContent, response.Code)
	require.Zero(t, ordinary.calls)
	seen := <-observations
	require.NoError(t, seen.err)
	require.Equal(t, policyregistry.ServingAssertionV2, seen.assertion.SchemaVersion)
	require.Equal(t, preview.Admission.Selection, seen.assertion.Admission.Selection)
	require.Equal(t, policyregistry.TargetStable, seen.assertion.Admission.Target)
	require.Equal(t, repo.identity.SubjectID, seen.assertion.TestPlan.SubjectID)
	require.Equal(t, session, seen.assertion.TestPlan.SessionID)
	for _, header := range []string{"Authorization", "x-api-key", "X-Weave-User-Email", "X-Weave-Serving-Target", policyregistry.TestPlanGrantHeader, policyregistry.TestPlanSessionHeader} {
		require.Empty(t, seen.headers.Get(header), header)
	}
	require.NoError(t, repo.RevokeTestLaunch(context.Background(), config.Launch.ID))
	denied := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"auto"}`))
	denied.Header.Set(auth.RouterKeyHeader, "rk_synthetic")
	denied.Header.Set(policyregistry.TestPlanGrantHeader, config.Grant)
	denied.Header.Set(policyregistry.TestPlanSessionHeader, session)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, denied)
	require.Equal(t, http.StatusServiceUnavailable, response.Code)
	require.Empty(t, observations, "revoked launch must never reach the worker")
	require.Zero(t, ordinary.calls)
}
