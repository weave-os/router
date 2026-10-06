package middleware

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/hmm/rosterdata"
)

const (
	admissionTestRoot       = "gs://admission-fixture/registry"
	admissionTestModel      = "gpt-5.6-sol"
	admissionTestArm        = providers.ProviderOpenAI + "/" + admissionTestModel
	admissionTestClass      = "low"
	admissionTestCredential = "rk_admission-fixture"
	admissionTestBody       = "{ \"model\":\"auto\",\n \"messages\":[{\"role\":\"user\",\"content\":\"hello\"}] }"
)

type admissionAttributionFunc func(context.Context, string, policyregistry.ServingAssertion) error

func (f admissionAttributionFunc) RecordServingRequest(ctx context.Context, requestID string, assertion policyregistry.ServingAssertion) error {
	return f(ctx, requestID, assertion)
}

type admissionManifestStore struct {
	policyregistry.ServingStore // Unexpected mutable-head reads fail the test instead of supplying a fallback.
	objects                     map[policyregistry.ObjectRef][]byte
	policy                      *rosterdata.Roster
	policyRef                   policyregistry.ObjectRef
	objectReads                 []policyregistry.ServingKind
	policyReads                 int
}

func (*admissionManifestStore) RootURI() string { return admissionTestRoot }

func (s *admissionManifestStore) ReadServingObject(_ context.Context, kind policyregistry.ServingKind, ref policyregistry.ObjectRef) (policyregistry.ServingManifest, []byte, error) {
	s.objectReads = append(s.objectReads, kind)
	payload, ok := s.objects[ref]
	if !ok {
		return nil, nil, policyregistry.ErrNotFound
	}
	manifest, err := policyregistry.DecodeServingManifest(payload, admissionTestRoot, kind)
	if err != nil {
		return nil, nil, err
	}
	return manifest, payload, nil
}

func (s *admissionManifestStore) ReadServingPolicy(_ context.Context, ref policyregistry.ObjectRef) (*rosterdata.Roster, error) {
	s.policyReads++
	if ref != s.policyRef {
		return nil, policyregistry.ErrNotFound
	}
	return s.policy, nil
}

func (s *admissionManifestStore) put(t *testing.T, kind policyregistry.ServingKind, manifest policyregistry.ServingManifest) policyregistry.ObjectRef {
	t.Helper()
	require.NoError(t, manifest.Validate(admissionTestRoot))
	payload, err := policyregistry.CanonicalBytes(manifest)
	require.NoError(t, err)
	digest := policyregistry.Digest(payload)
	ref := policyregistry.ObjectRef{URI: admissionTestRoot + "/router_serving/v1/" + string(kind) + "/sha256/" + digest + ".json", SHA256: digest, Generation: 1}
	s.objects[ref] = payload
	return ref
}

type admittedModelRouter struct{}

func (admittedModelRouter) Route(context.Context, router.Request) (router.Decision, error) {
	return router.Decision{Provider: providers.ProviderOpenAI, Model: admissionTestModel}, nil
}

func admissionMiddlewareFixture(t *testing.T) (*ServingAdmissionConfig, policyregistry.ServingAssertion, *admissionManifestStore, *int) {
	t.Helper()
	policy := &rosterdata.Roster{
		SchemaVersion: rosterdata.SchemaVersionPolicyV1, ClassOrder: []string{admissionTestClass},
		Ranking:  rosterdata.Ranking{Alpha: map[string]float64{admissionTestClass: .4}, AlphaMin: map[string]float64{admissionTestClass: .1}, AlphaMax: map[string]float64{admissionTestClass: .8}, QualityBiasNeutral: .7, WIIScoreVersion: "wii-v1", WIINormalizationSHA256: "wii", WPIScoreVersion: "wpi-v1", WPINormalizationSHA256: "wpi"},
		Clusters: map[string]rosterdata.Cluster{admissionTestClass: {ComplexityLabel: admissionTestClass, Arms: []string{admissionTestArm}, CostRefUSD: 1, LatencyRefMS: 1, ArmScores: map[string]float64{admissionTestArm: 1}, ArmIndices: map[string]rosterdata.ArmIndices{admissionTestArm: {WII: 80, WPI: 40}}}},
	}
	policyBytes, err := rosterdata.CanonicalBytes(policy)
	require.NoError(t, err)
	policySHA := policyregistry.Digest(policyBytes)
	policyRef := policyregistry.ObjectRef{URI: admissionTestRoot + "/router_policy/v1/policies/sha256/" + policySHA + ".json", SHA256: policySHA, Generation: 1}
	store := &admissionManifestStore{objects: make(map[policyregistry.ObjectRef][]byte), policy: policy, policyRef: policyRef}
	artifact := func(name string) policyregistry.ObjectRef {
		return policyregistry.ObjectRef{URI: admissionTestRoot + "/artifacts/" + name, SHA256: policyregistry.Digest([]byte(name)), Generation: 1}
	}
	classifierPackage := artifact("classifier-package")
	bundle := &policyregistry.ClassifierBundle{
		SchemaVersion: policyregistry.ServingClassifierV1, Package: classifierPackage, Configuration: artifact("classifier-config"), AuxiliaryModels: map[string]policyregistry.ObjectRef{},
		Identity: policyregistry.ClassifierIdentity{ArtifactID: "classifier", PackageSHA256: classifierPackage.SHA256, ImageDigest: "sha256:" + strings.Repeat("c", 64), WireSchema: policyregistry.ClassifierWireSchemaV4, ClassOrder: policy.ClassOrder, TaxonomySHA256: policyregistry.TaxonomyDigest(policy.ClassOrder)},
	}
	bundleRef := store.put(t, policyregistry.ServingClassifiers, bundle)
	release := &policyregistry.ServingRelease{
		SchemaVersion: policyregistry.ServingReleaseV1, RouterImageDigest: "sha256:" + strings.Repeat("d", 64), Classifier: bundleRef,
		Policy:       policyregistry.PolicyObject{URI: policyRef.URI, SHA256: policyRef.SHA256, Generation: policyRef.Generation, SchemaVersion: policy.SchemaVersion},
		Requirements: policyregistry.ServingRequirements{RuntimeContract: policyregistry.ManagedRuntimeContractV1, PolicySchema: policy.SchemaVersion, ClassifierWireSchema: bundle.Identity.WireSchema, TaxonomySHA256: bundle.Identity.TaxonomySHA256},
		Provenance:   policyregistry.ServingProvenance{RouterRevision: strings.Repeat("a", 40), WeaveRevision: strings.Repeat("b", 40), BuildAttestation: artifact("build-attestation")},
	}
	releaseRef := store.put(t, policyregistry.ServingReleases, release)
	binding := &policyregistry.DeploymentBinding{
		SchemaVersion: policyregistry.ServingBindingV1, Target: policyregistry.TargetStable, Project: "fixture-project", Region: "fixture-region", Release: releaseRef,
		Router:                 policyregistry.RevisionBinding{Name: "worker-0001", URL: "https://worker-0001.example", Audience: "https://worker.example", ImageDigest: release.RouterImageDigest, Configuration: artifact("worker-config")},
		Classifier:             policyregistry.RevisionBinding{Name: "classifier-0001", URL: "https://classifier-0001.example", Audience: "https://classifier.example", ImageDigest: bundle.Identity.ImageDigest, Configuration: bundle.Configuration},
		ClassifierBundleSHA256: bundleRef.SHA256, Attestation: artifact("binding-attestation"),
	}
	bindingRef := store.put(t, policyregistry.ServingBindings, binding)
	builds := new(int)
	cache, err := policyregistry.NewServingRuntimeCache(store, func(_ context.Context, candidate policyregistry.Candidate) (map[router.Strategy]router.Router, error) {
		*builds++
		require.Equal(t, releaseRef.SHA256, candidate.HeadSnapshot.Head.ReleaseSHA256)
		require.Same(t, policy, candidate.Policy)
		return map[router.Strategy]router.Router{router.StrategyHMM: admittedModelRouter{}}, nil
	})
	require.NoError(t, err)
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	digest, persistent := policyregistry.ServingConversationDigest("credential", "conversation")
	assertion := policyregistry.ServingAssertion{
		APIKeyID: "credential", Scope: policyregistry.AdmissionScope{InstallationID: "installation", CredentialIdentity: "credential", ConversationDigest: digest, Persistent: persistent},
		Admission: policyregistry.SessionReleaseBinding{Target: binding.Target, ActivationID: "activation-previous", Selection: policyregistry.ServingSelection{Release: releaseRef, Binding: bindingRef}, BindingGeneration: 3, CreatedAt: now, LastAdmittedAt: now},
	}
	decisions, err := policyregistry.NewAdmissionDecisionCache(&directAdmissionStore{assertion: assertion}, 10, time.Minute, time.Now)
	require.NoError(t, err)
	return &ServingAdmissionConfig{Decisions: decisions, Store: store, Cache: cache, Identity: policyregistry.WorkerIdentity{Target: binding.Target, Project: binding.Project, Region: binding.Region, Revision: binding.Router.Name, ImageDigest: binding.Router.ImageDigest, Configuration: binding.Router.Configuration}}, assertion, store, builds
}

func runAdmissionMiddleware(t *testing.T, cfg *ServingAdmissionConfig, assertion policyregistry.ServingAssertion, handler gin.HandlerFunc) *httptest.ResponseRecorder {
	return runAdmissionMiddlewareWithBody(t, cfg, assertion, admissionTestBody, handler)
}

func runAdmissionMiddlewareWithBody(t *testing.T, cfg *ServingAdmissionConfig, assertion policyregistry.ServingAssertion, body string, handler gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/v1/messages?original=1", strings.NewReader(body))
	request.Header.Set(auth.RouterKeyHeader, admissionTestCredential)
	request.Header.Set(policyregistry.ServingAssertionHeader, "forged-client-assertion")
	if assertion.TestPlan != nil {
		request = request.WithContext(policyregistry.WithServingAssertion(request.Context(), assertion))
	}
	request = request.WithContext(observability.WithRequestID(request.Context(), "request-original"))
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set(ctxKeyInstallation, &auth.Installation{ID: assertion.Scope.InstallationID})
		c.Set(ctxKeyAPIKey, &auth.APIKey{ID: assertion.APIKeyID})
		c.Next()
	})
	engine.POST("/v1/messages", WithServingAdmission(cfg), handler)
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	return response
}

func TestServingAdmissionRejectsMalformedBodyBeforeBinding(t *testing.T) {
	cfg, admitted, _, _ := admissionMiddlewareFixture(t)
	store := &directAdmissionStore{assertion: admitted}
	var err error
	cfg.Decisions, err = policyregistry.NewAdmissionDecisionCache(store, 10, time.Minute, time.Now)
	require.NoError(t, err)
	response := runAdmissionMiddlewareWithBody(t, cfg, policyregistry.ServingAssertion{}, `{"messages":`, func(*gin.Context) {
		t.Fatal("malformed request reached inference")
	})
	require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	require.Contains(t, response.Body.String(), "invalid_request_body")
	require.Zero(t, store.calls, "rejected requests must not create or refresh session bindings")
}

func TestServingAdmissionAllowsManagedRetirementReplyWithoutRegistryOrAttribution(t *testing.T) {
	cfg, _, _, _ := admissionMiddlewareFixture(t)
	cfg.Decisions = nil
	cfg.Store = nil
	cfg.Cache = nil
	cfg.Attribution = nil
	body := `{"model":"claude-sonnet-4-5","max_tokens":1,"messages":[{"role":"user","content":"/beta"}]}`
	response := runAdmissionMiddlewareWithBody(t, cfg, policyregistry.ServingAssertion{}, body, func(c *gin.Context) {
		t.Fatal("retired command must finish before admission or inference")
	})
	require.Equal(t, http.StatusOK, response.Code)
	require.Contains(t, response.Body.String(), "Beta has been retired")
}

func TestServingAdmissionPreservesBodyAndSnapshotWithAttributionBeforeDispatch(t *testing.T) {
	cfg, admitted, _, builds := admissionMiddlewareFixture(t)
	var recorded policyregistry.ServingAssertion
	var attributedSnapshot *policyregistry.Snapshot
	var order []string
	cfg.Attribution = admissionAttributionFunc(func(ctx context.Context, requestID string, assertion policyregistry.ServingAssertion) error {
		assert.Equal(t, "request-original", requestID)
		assert.Equal(t, admitted.Admission, assertion.Admission)
		assert.Equal(t, admitted.Scope, assertion.Scope)
		_, hasDeadline := ctx.Deadline()
		assert.True(t, hasDeadline)
		recorded = assertion
		attributedSnapshot = policyregistry.ServingSnapshotFromContext(ctx)
		require.NotNil(t, attributedSnapshot)
		order = append(order, "attribution")
		return nil
	})
	response := runAdmissionMiddleware(t, cfg, admitted, func(c *gin.Context) {
		order = append(order, "dispatch")
		body, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		assert.Equal(t, admissionTestBody, string(body))
		assert.Equal(t, recorded.BodySHA256, policyregistry.Digest(body))
		snapshot := policyregistry.ServingSnapshotFromContext(c.Request.Context())
		require.Same(t, attributedSnapshot, snapshot)
		assert.Equal(t, admitted.Admission.Selection.Release.SHA256, snapshot.HeadSnapshot.Head.ReleaseSHA256)
		decision, err := snapshot.Routers[router.StrategyHMM].Route(c.Request.Context(), router.Request{})
		require.NoError(t, err)
		assert.Equal(t, admissionTestModel, decision.Model)
		identity, ok := requestcontext.ServingIdentityFromContext(c.Request.Context())
		require.True(t, ok)
		assert.Equal(t, admitted.Admission.BindingGeneration, identity.BindingGeneration)
		assert.Equal(t, admitted.Admission.ActivationID, identity.ActivationID)
		assert.Equal(t, admitted.Admission.Selection.Binding.SHA256, identity.BindingID)
		assert.NotEmpty(t, identity.StateNamespace)
		c.Status(http.StatusAccepted)
	})
	assert.Equal(t, http.StatusAccepted, response.Code)
	assert.Equal(t, []string{"attribution", "dispatch"}, order)
	assert.Equal(t, 1, *builds)
}

func TestServingAdmissionAttributionFailurePreventsDispatch(t *testing.T) {
	for _, configured := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "storage failure"}[configured], func(t *testing.T) {
			cfg, admitted, _, _ := admissionMiddlewareFixture(t)
			if configured {
				cfg.Attribution = admissionAttributionFunc(func(context.Context, string, policyregistry.ServingAssertion) error {
					return errors.New("primary unavailable")
				})
			}
			response := runAdmissionMiddleware(t, cfg, admitted, func(*gin.Context) { t.Fatal("inference dispatched before attribution persisted") })
			assert.Equal(t, http.StatusServiceUnavailable, response.Code)
			assert.Contains(t, response.Body.String(), "serving_attribution_unavailable")
		})
	}
}

func TestServingAdmissionFleetMismatchPreventsSnapshotLoad(t *testing.T) {
	cfg, admitted, store, builds := admissionMiddlewareFixture(t)
	cfg.Identity.Target = policyregistry.TargetInternal
	cfg.Attribution = admissionAttributionFunc(func(context.Context, string, policyregistry.ServingAssertion) error {
		t.Fatal("rejected worker identity wrote attribution")
		return nil
	})
	response := runAdmissionMiddleware(t, cfg, admitted, func(*gin.Context) { t.Fatal("wrong worker dispatched inference") })
	assert.Equal(t, http.StatusForbidden, response.Code)
	assert.Contains(t, response.Body.String(), "serving_admission_rejected")
	assert.Empty(t, store.objectReads)
	assert.Zero(t, store.policyReads)
	assert.Zero(t, *builds)
}

func TestServingAdmissionDisabledDoesNotReadBodyOrRequireDependencies(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(admissionTestBody))
	originalBody := &struct{ io.ReadCloser }{request.Body}
	request.Body = originalBody
	request.Header.Set(policyregistry.ServingAssertionHeader, "untrusted-legacy-header")
	engine := gin.New()
	engine.POST("/v1/messages", WithServingAdmission(nil), func(c *gin.Context) {
		assert.Same(t, originalBody, c.Request.Body)
		assert.Nil(t, policyregistry.ServingSnapshotFromContext(c.Request.Context()))
		_, exists := requestcontext.ServingIdentityFromContext(c.Request.Context())
		assert.False(t, exists)
		body, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		assert.Equal(t, admissionTestBody, string(body))
		c.Status(http.StatusAccepted)
	})
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	assert.Equal(t, http.StatusAccepted, response.Code)
}

type directAdmissionStore struct {
	calls     int
	assertion policyregistry.ServingAssertion
	err       error
}

func (s *directAdmissionStore) Admit(_ context.Context, installationID, keyID, _ string, _ policyregistry.AdmissionDecision) (policyregistry.AdmissionScope, policyregistry.SessionReleaseBinding, error) {
	s.calls++
	if s.err != nil {
		return policyregistry.AdmissionScope{}, policyregistry.SessionReleaseBinding{}, s.err
	}
	if installationID != s.assertion.Scope.InstallationID || keyID != s.assertion.APIKeyID {
		return policyregistry.AdmissionScope{}, policyregistry.SessionReleaseBinding{}, errors.New("identity mismatch")
	}
	return s.assertion.Scope, s.assertion.Admission, nil
}

func TestDirectAdmissionMapsCredentialFailuresAndInfrastructureFailures(t *testing.T) {
	for _, test := range []struct {
		name       string
		err        error
		wantStatus int
		wantBody   string
	}{
		{name: "invalid credential", err: auth.ErrInvalidToken, wantStatus: http.StatusUnauthorized, wantBody: "invalid_key"},
		{name: "revoked subject", err: auth.ErrPersonalCredentialRequired, wantStatus: http.StatusUnauthorized, wantBody: "invalid_key"},
		{name: "registry outage", err: errors.New("primary unavailable"), wantStatus: http.StatusServiceUnavailable, wantBody: "serving_admission_unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg, admitted, _, _ := admissionMiddlewareFixture(t)
			store := &directAdmissionStore{assertion: admitted, err: test.err}
			var err error
			cfg.Decisions, err = policyregistry.NewAdmissionDecisionCache(store, 10, time.Minute, time.Now)
			require.NoError(t, err)
			response := runAdmissionMiddleware(t, cfg, policyregistry.ServingAssertion{}, func(*gin.Context) { t.Fatal("failed admission dispatched") })
			require.Equal(t, test.wantStatus, response.Code, response.Body.String())
			require.Contains(t, response.Body.String(), test.wantBody)
		})
	}
}
func TestDirectAdmissionRetainsPolicyAcrossWorkerCodeRelease(t *testing.T) {
	cfg, admitted, _, _ := admissionMiddlewareFixture(t)
	primary := &directAdmissionStore{assertion: admitted}
	var err error
	cfg.Decisions, err = policyregistry.NewAdmissionDecisionCache(primary, 10, time.Minute, time.Now)
	require.NoError(t, err)
	cfg.Identity.Revision = "worker-0002"
	cfg.Identity.ImageDigest = "sha256:" + strings.Repeat("e", 64)
	cfg.Attribution = admissionAttributionFunc(func(_ context.Context, _ string, got policyregistry.ServingAssertion) error {
		require.Equal(t, admitted.Admission, got.Admission)
		return nil
	})
	for range 2 {
		response := runAdmissionMiddleware(t, cfg, admitted, func(c *gin.Context) {
			require.NotNil(t, policyregistry.ServingSnapshotFromContext(c.Request.Context()))
			body, err := io.ReadAll(c.Request.Body)
			require.NoError(t, err)
			require.Equal(t, admissionTestBody, string(body))
			c.Status(http.StatusNoContent)
		})
		require.Equal(t, http.StatusNoContent, response.Code, response.Body.String())
	}
	// No canonical session in this fixture: each turn must consult primary.
	require.Equal(t, 2, primary.calls)
}
func TestDirectAdmissionRejectsOtherFleet(t *testing.T) {
	cfg, admitted, _, _ := admissionMiddlewareFixture(t)
	primary := &directAdmissionStore{assertion: admitted}
	var err error
	cfg.Decisions, err = policyregistry.NewAdmissionDecisionCache(primary, 10, time.Minute, time.Now)
	require.NoError(t, err)
	cfg.Identity.Target = policyregistry.TargetInternal
	response := runAdmissionMiddleware(t, cfg, admitted, func(c *gin.Context) { t.Fatal("cross-fleet dispatch") })
	require.Equal(t, http.StatusForbidden, response.Code)
}
