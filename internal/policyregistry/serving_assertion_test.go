package policyregistry_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/policyregistry"
)

func TestServingAssertionBindsRequestAndExpires(t *testing.T) {
	_, _, set := controllerFixture(t)
	now := servingEpoch
	signer, err := policyregistry.NewAssertionSigner([]byte(strings.Repeat("k", 32)), func() time.Time { return now })
	require.NoError(t, err)
	body := []byte(`{"model":"auto","stream":true}`)
	r := httptest.NewRequest(http.MethodPost, "https://router.example/v1/messages?beta=true", strings.NewReader(string(body)))
	admission := policyregistry.SessionReleaseBinding{Target: policyregistry.TargetStable, ActivationID: "activation", Selection: set.Default, BindingGeneration: 1}
	assertion := policyregistry.ServingAssertion{APIKeyID: "key", Scope: policyregistry.AdmissionScope{InstallationID: "installation", CredentialIdentity: "subject"}, Admission: admission}
	encoded, err := signer.Sign(assertion, r, body, "rk_credential")
	require.NoError(t, err)
	verified, err := signer.Verify(encoded, r, body, "rk_credential")
	require.NoError(t, err)
	assert.Equal(t, admission, verified.Admission)
	for _, test := range []struct {
		name   string
		mutate func(*http.Request)
	}{
		{"method", func(r *http.Request) { r.Method = http.MethodGet }},
		{"path", func(r *http.Request) { r.URL.Path = "/v1/responses" }},
		{"query", func(r *http.Request) { r.URL.RawQuery = "beta=false" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := r.Clone(r.Context())
			test.mutate(changed)
			_, err := signer.Verify(encoded, changed, body, "rk_credential")
			require.Error(t, err)
		})
	}
	_, err = signer.Verify(encoded, r, []byte(`{}`), "rk_credential")
	require.Error(t, err)
	_, err = signer.Verify(encoded, r, body, "rk_other")
	require.Error(t, err)
	other, err := policyregistry.NewAssertionSigner([]byte(strings.Repeat("x", 32)), func() time.Time { return now })
	require.NoError(t, err)
	_, err = other.Verify(encoded, r, body, "rk_credential")
	require.Error(t, err)
	now = servingEpoch.Add(2 * time.Minute)
	_, err = signer.Verify(encoded, r, body, "rk_credential")
	require.ErrorContains(t, err, "window")
}

func TestWorkerAdmissionRequiresExactLocalIdentity(t *testing.T) {
	store, _, set := controllerFixture(t)
	binding := store.object(t, policyregistry.ServingBindings, set.Default.Binding).(*policyregistry.DeploymentBinding)
	assertion := policyregistry.ServingAssertion{APIKeyID: "key", Scope: policyregistry.AdmissionScope{InstallationID: "installation"}, Admission: policyregistry.SessionReleaseBinding{Target: policyregistry.TargetStable, Selection: set.Default}}
	identity := policyregistry.WorkerIdentity{Target: binding.Target, Project: binding.Project, Region: binding.Region, Revision: binding.Router.Name, ImageDigest: binding.Router.ImageDigest, Configuration: binding.Router.Configuration}
	_, err := policyregistry.ValidateWorkerAdmission(context.Background(), store, identity, assertion, "installation", "key")
	require.NoError(t, err)
	for _, test := range []struct {
		name   string
		mutate func(*policyregistry.WorkerIdentity)
	}{
		{"target", func(w *policyregistry.WorkerIdentity) { w.Target = policyregistry.TargetInternal }},
		{"image", func(w *policyregistry.WorkerIdentity) { w.ImageDigest = "sha256:" + strings.Repeat("f", 64) }},
		{"revision", func(w *policyregistry.WorkerIdentity) { w.Revision = "worker-0002" }},
		{"configuration", func(w *policyregistry.WorkerIdentity) { w.Configuration = artifactRef("other-config") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := identity
			test.mutate(&changed)
			_, err := policyregistry.ValidateWorkerAdmission(context.Background(), store, changed, assertion, "installation", "key")
			require.Error(t, err)
		})
	}
	_, err = policyregistry.ValidateWorkerAdmission(context.Background(), store, identity, assertion, "other", "key")
	require.Error(t, err)
	_, err = policyregistry.ValidateWorkerAdmission(context.Background(), store, identity, assertion, "installation", "other")
	require.Error(t, err)
}
