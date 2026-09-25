package middleware

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
)

func discoveryMetadata(t *testing.T, admitted policyregistry.ServingAssertion) string {
	t.Helper()
	encoded, err := policyregistry.EncodeDiscoverySelection(policyregistry.WorkerValidationRequest{Target: admitted.Admission.Target, Selection: admitted.Admission.Selection})
	require.NoError(t, err)
	return encoded
}

func runDiscoveryMiddleware(cfg *ServingAdmissionConfig, encoded string, discovery gin.HandlerFunc) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "/internal/v1/router/hmm-roster?strategy="+string(router.StrategyHMM), nil)
	request.Header.Set(policyregistry.DiscoverySelectionHeader, encoded)
	engine := gin.New()
	chain := gin.HandlersChain{WithServingDiscovery(cfg), discovery}
	engine.GET("/internal/v1/router/hmm-roster", chain...)
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	return response
}

func TestServingDiscoveryLoadsExactSnapshotWithoutInferenceIdentityOrAttribution(t *testing.T) {
	cfg, admitted, _, builds := admissionMiddlewareFixture(t)
	cfg.Signer = nil
	cfg.Attribution = admissionAttributionFunc(func(context.Context, string, policyregistry.ServingAssertion) error {
		t.Fatal("private discovery wrote inference attribution")
		return nil
	})
	response := runDiscoveryMiddleware(cfg, discoveryMetadata(t, admitted), func(c *gin.Context) {
		assert.Nil(t, APIKeyFrom(c))
		assert.Nil(t, InstallationFrom(c))
		_, hasIdentity := requestcontext.ServingIdentityFromContext(c.Request.Context())
		assert.False(t, hasIdentity)
		snapshot := policyregistry.ServingSnapshotFromContext(c.Request.Context())
		require.NotNil(t, snapshot)
		assert.Equal(t, admitted.Admission.Selection.Release.SHA256, snapshot.HeadSnapshot.Head.ReleaseSHA256)
		roster, err := (policyregistry.AdmittedRosterSource{}).Roster(c.Request.Context())
		require.NoError(t, err)
		c.JSON(http.StatusOK, gin.H{"arms": roster})
	})
	assert.Equal(t, http.StatusOK, response.Code)
	assert.JSONEq(t, `{"arms":["`+admissionTestArm+`"]}`, response.Body.String())
	assert.Equal(t, 1, *builds)
}

func TestServingDiscoveryRejectsMissingInvalidAndProfileMetadataBeforeLoading(t *testing.T) {
	for _, name := range []string{"missing", "malformed", "oversized", "profile key", "profile revision", "private target"} {
		t.Run(name, func(t *testing.T) {
			cfg, admitted, store, builds := admissionMiddlewareFixture(t)
			request := policyregistry.WorkerValidationRequest{Target: admitted.Admission.Target, Selection: admitted.Admission.Selection}
			encoded := ""
			switch name {
			case "malformed":
				encoded = "invalid"
			case "oversized":
				encoded = strings.Repeat("x", 16*1024+1)
			case "profile key":
				request.ProfileKey = "10000000-0000-4000-8000-000000000001"
			case "profile revision":
				request.Selection.Profile = &request.Selection.Binding
			case "private target":
				request.Target = policyregistry.TargetInternal
			}
			if name == "profile key" || name == "profile revision" || name == "private target" {
				payload, err := policyregistry.CanonicalBytes(request)
				require.NoError(t, err)
				encoded = base64.RawURLEncoding.EncodeToString(payload)
			}
			response := runDiscoveryMiddleware(cfg, encoded, func(*gin.Context) { t.Fatal("invalid discovery reached catalog") })
			assert.Equal(t, http.StatusUnauthorized, response.Code)
			assert.Contains(t, response.Body.String(), "discovery_selection_required")
			assert.Empty(t, store.objectReads)
			assert.Zero(t, store.policyReads)
			assert.Zero(t, *builds)
		})
	}
}

func TestServingDiscoveryRejectsWrongWorkerBeforeSnapshotLoad(t *testing.T) {
	cfg, admitted, store, builds := admissionMiddlewareFixture(t)
	cfg.Identity.Revision = "worker-other"
	response := runDiscoveryMiddleware(cfg, discoveryMetadata(t, admitted), func(*gin.Context) { t.Fatal("wrong worker served discovery") })
	assert.Equal(t, http.StatusForbidden, response.Code)
	assert.Contains(t, response.Body.String(), "discovery_selection_rejected")
	assert.Equal(t, []policyregistry.ServingKind{policyregistry.ServingBindings}, store.objectReads)
	assert.Zero(t, store.policyReads)
	assert.Zero(t, *builds)
}

func TestServingDiscoveryUnavailableSelectionDoesNotFallback(t *testing.T) {
	for _, name := range []string{"missing binding", "missing release", "missing policy", "missing runtime", "failed runtime"} {
		t.Run(name, func(t *testing.T) {
			cfg, admitted, store, builds := admissionMiddlewareFixture(t)
			switch name {
			case "missing binding":
				delete(store.objects, admitted.Admission.Selection.Binding)
			case "missing release":
				delete(store.objects, admitted.Admission.Selection.Release)
			case "missing policy":
				store.policyRef = policyregistry.ObjectRef{}
			case "missing runtime":
				cfg.Cache = nil
			case "failed runtime":
				var err error
				cfg.Cache, err = policyregistry.NewServingRuntimeCache(store, func(context.Context, policyregistry.Candidate) (map[router.Strategy]router.Router, error) {
					return nil, errors.New("runtime unavailable")
				})
				require.NoError(t, err)
			}
			response := runDiscoveryMiddleware(cfg, discoveryMetadata(t, admitted), func(*gin.Context) { t.Fatal("unavailable snapshot served discovery") })
			assert.Equal(t, http.StatusServiceUnavailable, response.Code)
			assert.Contains(t, response.Body.String(), "discovery_snapshot_unavailable")
			assert.Zero(t, *builds)
		})
	}
}

type canceledDiscoveryStore struct {
	policyregistry.ServingStore
	observed error
}

func (s *canceledDiscoveryStore) ReadServingObject(ctx context.Context, _ policyregistry.ServingKind, _ policyregistry.ObjectRef) (policyregistry.ServingManifest, []byte, error) {
	<-ctx.Done()
	s.observed = ctx.Err()
	return nil, nil, s.observed
}

func TestServingDiscoveryUsesRequestDeadlineForRegistryReads(t *testing.T) {
	cfg, admitted, store, builds := admissionMiddlewareFixture(t)
	blockedStore := &canceledDiscoveryStore{ServingStore: store}
	cfg.Store = blockedStore
	engine := gin.New()
	engine.Use(WithTimeout(0))
	chain := gin.HandlersChain{WithServingDiscovery(cfg), func(*gin.Context) { t.Fatal("canceled discovery served a snapshot") }}
	engine.GET("/internal/v1/router/models", chain...)
	request := httptest.NewRequest(http.MethodGet, "/internal/v1/router/models", nil)
	request.Header.Set(policyregistry.DiscoverySelectionHeader, discoveryMetadata(t, admitted))
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	assert.Equal(t, http.StatusServiceUnavailable, response.Code)
	assert.ErrorIs(t, blockedStore.observed, context.DeadlineExceeded)
	assert.Zero(t, *builds)
}
