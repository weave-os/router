package gateway_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/gateway"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/cluster"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/router/hmm/armid"
	"weave-os/router/internal/router/hmm/rosterdata"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/server"
	"weave-os/router/internal/server/middleware"
)

const (
	discoveryCredential = "rk_discovery-fixture"
	discoveryProfileKey = "10000000-0000-4000-8000-000000000001"
	discoveryClass      = string(escalation.Low)
	discoveryOpenAIArm  = providers.ProviderOpenAI + "/" + string(catalog.ModelIDGPT55)
)

var (
	discoveryAnthropicArm = armid.ForModel(catalog.Model{ID: catalog.ModelIDClaudeHaiku45.String()})
	discoveryProfileArm   = armid.ForModel(catalog.Model{ID: catalog.ModelIDClaudeOpus48.String()})
)

type discoveryStore struct {
	policyregistry.ServingStore
	mu         sync.RWMutex
	objects    map[policyregistry.ObjectRef][]byte
	policies   map[policyregistry.ObjectRef]*rosterdata.Roster
	state      policyregistry.ServingStateSnapshot
	stateReads atomic.Int32
}

func (*discoveryStore) RootURI() string { return registryRoot }

func (s *discoveryStore) ReadServingObject(_ context.Context, kind policyregistry.ServingKind, ref policyregistry.ObjectRef) (policyregistry.ServingManifest, []byte, error) {
	s.mu.RLock()
	payload, exists := s.objects[ref]
	s.mu.RUnlock()
	if !exists {
		return nil, nil, policyregistry.ErrNotFound
	}
	manifest, err := policyregistry.DecodeServingObject(payload, registryRoot, kind, ref)
	return manifest, payload, err
}

func (s *discoveryStore) ReadServingPolicy(_ context.Context, ref policyregistry.ObjectRef) (*rosterdata.Roster, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	roster, exists := s.policies[ref]
	if !exists {
		return nil, policyregistry.ErrNotFound
	}
	return roster, nil
}

func (s *discoveryStore) ReadServingState(_ context.Context, target policyregistry.ServingTarget) (policyregistry.ServingStateSnapshot, error) {
	s.stateReads.Add(1)
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.state.State.Target != target {
		return policyregistry.ServingStateSnapshot{}, policyregistry.ErrNotFound
	}
	return s.state, nil
}

func (s *discoveryStore) publish(t *testing.T, kind policyregistry.ServingKind, manifest policyregistry.ServingManifest) policyregistry.ObjectRef {
	t.Helper()
	require.NoError(t, manifest.Validate(registryRoot))
	payload, err := policyregistry.CanonicalBytes(manifest)
	require.NoError(t, err)
	digest := policyregistry.Digest(payload)
	ref := policyregistry.ObjectRef{URI: registryRoot + "/artifacts/" + digest + ".json", SHA256: digest, Generation: 1}
	_, err = policyregistry.DecodeServingObject(payload, registryRoot, kind, ref)
	require.NoError(t, err)
	s.objects[ref] = payload
	return ref
}

func (s *discoveryStore) activate(target policyregistry.ServingTarget, selectionSet policyregistry.ObjectRef) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sequence := s.state.Generation + 1
	id := uuid.NewString()
	activation := policyregistry.Activation{
		ID: id, Sequence: sequence, SelectionSet: selectionSet, ActivatedAt: time.Now().Add(-time.Minute),
		Proposal: servingRef(policyregistry.ServingProposals, []byte("discovery-proposal")), RequestID: uuid.NewString(), Actor: "test-operator", WorkflowActor: "test-workflow",
	}
	s.state = policyregistry.ServingStateSnapshot{Generation: sequence, State: policyregistry.ServingControlState{
		SchemaVersion: policyregistry.ServingControlStateV1, Target: target, CurrentActivationID: id, Sequence: sequence,
		Activations: map[string]policyregistry.Activation{id: activation},
	}}
}

type discoveryCredentials struct {
	auth.APIKeyRepository
	gatewayCalls atomic.Int32
	workerCalls  atomic.Int32
}

func (c *discoveryCredentials) VerifyRoutingCredential(_ context.Context, token string) (*auth.Installation, *auth.APIKey, error) {
	c.gatewayCalls.Add(1)
	if token != discoveryCredential {
		return nil, nil, auth.ErrInvalidToken
	}
	return &auth.Installation{ID: "installation"}, &auth.APIKey{ID: "key", InstallationID: "installation", Scope: auth.ScopeRouting}, nil
}

func (c *discoveryCredentials) GetActiveByHashWithInstallation(_ context.Context, hash string) (*auth.APIKey, *auth.Installation, error) {
	c.workerCalls.Add(1)
	if hash != auth.HashAPIKeySHA256(discoveryCredential) {
		return nil, nil, sql.ErrNoRows
	}
	return &auth.APIKey{ID: "key", InstallationID: "installation", Scope: auth.ScopeRouting}, &auth.Installation{ID: "installation"}, nil
}

func (*discoveryCredentials) MarkUsed(context.Context, string) (bool, error) { return false, nil }

type discoveryInstallations struct{ auth.InstallationRepository }

func (discoveryInstallations) MarkFirstRequestServed(context.Context, string) error { return nil }

type discoveryAccounting struct {
	t            *testing.T
	admission    policyregistry.SessionReleaseBinding
	admissions   atomic.Int32
	attributions atomic.Int32
}

func (a *discoveryAccounting) Admit(_ context.Context, installation, key, conversation string, _ policyregistry.AdmissionDecision) (policyregistry.AdmissionScope, policyregistry.SessionReleaseBinding, error) {
	a.admissions.Add(1)
	assert.Equal(a.t, "installation", installation)
	assert.Equal(a.t, "key", key)
	digest, persistent := policyregistry.ServingConversationDigest(key, conversation)
	return policyregistry.AdmissionScope{InstallationID: installation, CredentialIdentity: key, ConversationDigest: digest, Persistent: persistent}, a.admission, nil
}

func (a *discoveryAccounting) RecordServingRequest(ctx context.Context, requestID string, assertion policyregistry.ServingAssertion) error {
	a.attributions.Add(1)
	assert.NotEmpty(a.t, requestID)
	assert.Equal(a.t, a.admission, assertion.Admission)
	assert.NotNil(a.t, policyregistry.ServingSnapshotFromContext(ctx))
	_, hasIdentity := requestcontext.ServingIdentityFromContext(ctx)
	assert.True(a.t, hasIdentity)
	return nil
}

type discoveryRouter struct {
	t               *testing.T
	honorsPreferred bool
}

func (r discoveryRouter) Route(context.Context, router.Request) (router.Decision, error) {
	r.t.Error("discovery invoked inference routing")
	return router.Decision{}, errors.New("inference is forbidden in discovery")
}

func (r discoveryRouter) CurrentCapabilities() policy.Capabilities {
	return policy.Capabilities{SchemaVersion: policy.SchemaVersionV4, HonorsPreferredModels: r.honorsPreferred, ReportsRankedFallback: true, SupportsRoutingDistribution: true}
}

type discoveryModels struct{}

func (discoveryModels) DefaultDeployedModels() []cluster.DeployedEntry {
	return hmm.DeployedModelsForRosterIDs([]string{discoveryOpenAIArm, discoveryAnthropicArm})
}

func (discoveryModels) HMMDeployedModels(ctx context.Context) ([]cluster.DeployedEntry, error) {
	arms, err := (policyregistry.AdmittedRosterSource{}).Roster(ctx)
	if err != nil {
		return nil, err
	}
	return hmm.DeployedModelsForRosterIDs(arms), nil
}

type discoveryFixture struct {
	forwarder         *gateway.Handler
	worker            *gin.Engine
	store             *discoveryStore
	credentials       *discoveryCredentials
	accounting        *discoveryAccounting
	initial           policyregistry.ServingSelection
	next              policyregistry.ServingSelection
	profile           policyregistry.ServingSelection
	initialPolicy     policyregistry.PolicyObject
	workerCalls       atomic.Int32
	activateNextOnHop atomic.Bool
}

func newDiscoveryFixture(t *testing.T, target policyregistry.ServingTarget) *discoveryFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	t.Setenv("ROUTER_INTERNAL_SERVICE_TOKEN", "")
	t.Setenv("ROUTER_DEFAULT_STRATEGY", string(router.StrategyHMM))
	observability.Get()
	f := &discoveryFixture{
		worker: gin.New(), credentials: &discoveryCredentials{}, accounting: &discoveryAccounting{t: t},
		store: &discoveryStore{objects: make(map[policyregistry.ObjectRef][]byte), policies: make(map[policyregistry.ObjectRef]*rosterdata.Roster)},
	}
	f.worker.Use(observability.Middleware())
	f.worker.Use(func(c *gin.Context) {
		c.Next()
		if c.Writer.Status() == http.StatusOK && auth.RoutingTokenFromHeaders(c.Request.Header) == "" {
			assert.NotNil(t, policyregistry.ServingSnapshotFromContext(c.Request.Context()))
			_, hasIdentity := requestcontext.ServingIdentityFromContext(c.Request.Context())
			assert.False(t, hasIdentity)
			assert.Nil(t, middleware.APIKeyFrom(c))
			assert.Nil(t, middleware.InstallationFrom(c))
		}
	})
	worker := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.workerCalls.Add(1)
		if !assert.Equal(t, "Bearer gateway-iam", r.Header.Get(policyregistry.ServerlessAuthorizationHeader)) {
			http.Error(w, "private gateway identity required", http.StatusForbidden)
			return
		}
		assert.Empty(t, r.Header.Get("X-Weave-Serving-Target"))
		assert.Empty(t, r.Header.Get(gateway.DiscoveryServiceAuthorizationHeader))
		if strings.HasPrefix(r.URL.Path, "/internal/v1/router/") {
			assert.Empty(t, auth.RoutingTokenFromHeaders(r.Header))
		}
		if auth.RoutingTokenFromHeaders(r.Header) == "" {
			assert.Empty(t, r.Header.Get(policyregistry.ServingAssertionHeader))
		}
		if f.activateNextOnHop.CompareAndSwap(true, false) {
			f.store.activate(target, f.next.Binding)
		}
		w.Header().Set(policyregistry.DiscoverySelectionHeader, "private-response-selection")
		w.Header().Set(policyregistry.ServingAssertionHeader, "private-response-assertion")
		w.Header().Set(policyregistry.ServerlessAuthorizationHeader, "private-response-identity")
		f.worker.ServeHTTP(w, r)
	}))
	t.Cleanup(worker.Close)
	imageDigest := "sha256:" + strings.Repeat("a", 64)
	classifierPackage := artifact("discovery-classifier-package")
	classifier := policyregistry.ClassifierComponent{
		Package: classifierPackage, Configuration: artifact("discovery-classifier-config"), AuxiliaryModels: map[string]policyregistry.ObjectRef{},
		Identity: policyregistry.ClassifierIdentity{ArtifactID: "discovery-classifier", PackageSHA256: classifierPackage.SHA256, ImageDigest: imageDigest, WireSchema: policyregistry.ClassifierWireSchemaV4, ClassOrder: []string{discoveryClass}, TaxonomySHA256: policyregistry.TaxonomyDigest([]string{discoveryClass})},
	}
	binding := policyregistry.LaneBinding{
		Project: "fixture-project", Region: "fixture-region", Attestation: artifact("discovery-attestation"),
		Router:     policyregistry.RevisionBinding{Name: "worker-0001", URL: worker.URL, Audience: "https://worker.example", ImageDigest: imageDigest, Configuration: artifact("discovery-worker-config")},
		Classifier: policyregistry.RevisionBinding{Name: "classifier-0001", URL: "https://classifier-0001.example", Audience: "https://classifier.example", ImageDigest: imageDigest, Configuration: classifier.Configuration},
	}
	makeLane := func(arms []string) (policyregistry.ServingLane, policyregistry.CandidateV2) {
		roster := &rosterdata.Roster{
			SchemaVersion: rosterdata.SchemaVersionPolicyV1, ClassOrder: []string{discoveryClass},
			Ranking:  rosterdata.Ranking{Alpha: map[string]float64{discoveryClass: .4}, AlphaMin: map[string]float64{discoveryClass: .1}, AlphaMax: map[string]float64{discoveryClass: .8}, QualityBiasNeutral: .7, WIIScoreVersion: "wii-v1", WIINormalizationSHA256: "wii", WPIScoreVersion: "wpi-v1", WPINormalizationSHA256: "wpi"},
			Clusters: map[string]rosterdata.Cluster{discoveryClass: {ComplexityLabel: discoveryClass, Arms: arms, CostRefUSD: 1, LatencyRefMS: 1, ArmScores: map[string]float64{}, ArmIndices: map[string]rosterdata.ArmIndices{}}},
		}
		for i, arm := range arms {
			roster.Clusters[discoveryClass].ArmScores[arm] = 1
			roster.Clusters[discoveryClass].ArmIndices[arm] = rosterdata.ArmIndices{WII: 80 - float64(i)*20, WPI: 40 + float64(i)*20}
		}
		policyBytes, err := rosterdata.CanonicalBytes(roster)
		require.NoError(t, err)
		_, err = rosterdata.ParseValidated(policyBytes)
		require.NoError(t, err)
		digest := policyregistry.Digest(policyBytes)
		ref := policyregistry.ObjectRef{URI: registryRoot + "/router_policy/v1/policies/sha256/" + digest + ".json", SHA256: digest, Generation: 1}
		f.store.policies[ref] = roster
		candidate := policyregistry.CandidateV2{SchemaVersion: policyregistry.ServingCandidateV2, CandidateComposition: policyregistry.CandidateComposition{
			RouterImageDigest: imageDigest, Classifier: classifier,
			Policy:       policyregistry.PolicyObject{URI: ref.URI, SHA256: ref.SHA256, Generation: ref.Generation, SchemaVersion: roster.SchemaVersion},
			Requirements: policyregistry.ServingRequirements{RuntimeContract: policyregistry.ManagedRuntimeContractV1, PolicySchema: roster.SchemaVersion, ClassifierWireSchema: classifier.Identity.WireSchema, TaxonomySHA256: classifier.Identity.TaxonomySHA256},
			Provenance:   policyregistry.ServingProvenance{RouterRevision: strings.Repeat("b", 40), WeaveRevision: strings.Repeat("c", 40), BuildAttestation: artifact("discovery-build-attestation")},
		}}
		return policyregistry.ServingLane{Candidate: f.store.publish(t, policyregistry.ServingCandidate, candidate), LaneBinding: binding}, candidate
	}
	initialLane, initialCandidate := makeLane([]string{discoveryOpenAIArm, discoveryAnthropicArm})
	nextLane, _ := makeLane([]string{discoveryAnthropicArm})
	profileLane, profileCandidate := makeLane([]string{discoveryProfileArm})
	profileLane.ProfileKey, profileLane.ProfilePolicy, profileLane.ProfileRequirements = discoveryProfileKey, &profileCandidate.Policy, &profileCandidate.Requirements
	selectionSet := policyregistry.SelectionSetV2{SchemaVersion: policyregistry.ServingSelectionSetV2, Target: target, Default: initialLane, Profiles: map[string]policyregistry.ServingLane{discoveryProfileKey: profileLane}}
	selectionSetRef := f.store.publish(t, policyregistry.ServingSelectionSet, selectionSet)
	f.initial = policyregistry.ServingSelection{Release: initialLane.Candidate, Binding: selectionSetRef}
	f.profile = policyregistry.ServingSelection{Release: profileLane.Candidate, Binding: selectionSetRef, Profile: &selectionSetRef}
	f.initialPolicy = initialCandidate.Policy
	selectionSet.Default = nextLane
	f.next = policyregistry.ServingSelection{Release: nextLane.Candidate, Binding: f.store.publish(t, policyregistry.ServingSelectionSet, selectionSet)}
	f.store.activate(target, selectionSetRef)
	require.NoError(t, f.store.state.State.Validate(registryRoot, target))
	cache, err := policyregistry.NewServingRuntimeCache(f.store, func(_ context.Context, candidate policyregistry.Candidate) (map[router.Strategy]router.Router, error) {
		return map[router.Strategy]router.Router{router.StrategyHMM: discoveryRouter{t: t, honorsPreferred: candidate.Release.Policy.SHA256 == initialCandidate.Policy.SHA256}}, nil
	})
	require.NoError(t, err)
	signer, err := policyregistry.NewAssertionSigner([]byte(strings.Repeat("s", 32)), time.Now)
	require.NoError(t, err)
	now := time.Now().UTC()
	f.accounting.admission = policyregistry.SessionReleaseBinding{Target: target, ProfileKey: discoveryProfileKey, ActivationID: f.store.state.State.CurrentActivationID, Selection: f.profile, BindingGeneration: 1, CreatedAt: now, LastAdmittedAt: now}
	authSvc := auth.NewService(discoveryInstallations{}, f.credentials, nil, nil, auth.NoOpAPIKeyCache{}, nil, time.Now)
	bootstrap := &policyregistry.Snapshot{Routers: map[router.Strategy]router.Router{router.StrategyHMM: discoveryRouter{t: t}}}
	admittedRouter := policyregistry.NewAdmittedRouter(router.StrategyHMM, bootstrap)
	proxySvc := proxy.NewService(discoveryRouter{t: t}, nil, nil, false, nil, nil, false, "", "", nil).
		WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyHMM, Router: admittedRouter, Unavailable: policyregistry.ErrNoActivePolicy})
	cfg := &middleware.ServingAdmissionConfig{
		Signer: signer, Store: f.store, Cache: cache, Attribution: f.accounting,
		Identity: policyregistry.WorkerIdentity{Target: target, Project: binding.Project, Region: binding.Region, Revision: binding.Router.Name, ImageDigest: imageDigest, Configuration: binding.Router.Configuration},
	}
	server.RegisterWithFeatures(f.worker, authSvc, proxySvc, discoveryModels{}, discoveryModels{}, server.DeploymentModeManaged, nil, nil, map[router.Strategy]policy.RosterSource{router.StrategyHMM: policyregistry.AdmittedRosterSource{}}, nil, server.Features{ServingAdmission: cfg})
	environment, err := target.Environment()
	require.NoError(t, err)
	f.forwarder, err = gateway.NewHandler(f.credentials, f.accounting, f.store, signer, revisionAuthorizer{}, worker.Client().Transport, gateway.ProductSurfaces{Environment: environment, Analytics: &analyticsVerifier{err: auth.ErrInvalidToken}, Discovery: discoveryServiceIdentity{}})
	require.NoError(t, err)
	return f
}

func (f *discoveryFixture) request(t *testing.T, method, path string, headers http.Header) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, nil)
	if headers != nil {
		request.Header = headers.Clone()
	}
	response := httptest.NewRecorder()
	f.forwarder.ServeHTTP(response, request)
	assert.Empty(t, response.Header().Get(policyregistry.DiscoverySelectionHeader))
	assert.Empty(t, response.Header().Get(policyregistry.ServingAssertionHeader))
	assert.Empty(t, response.Header().Get(policyregistry.ServerlessAuthorizationHeader))
	return response
}

type discoveryServiceIdentity struct{}

func (discoveryServiceIdentity) VerifyServiceIdentity(_ context.Context, token string) error {
	if token != "backend-identity" {
		return errors.New("untrusted backend identity")
	}
	return nil
}

func (f *discoveryFixture) privateRequest(t *testing.T, method, path string, headers http.Header) *httptest.ResponseRecorder {
	t.Helper()
	parsed, err := url.Parse("/internal" + path)
	require.NoError(t, err)
	query := parsed.Query()
	if query.Get("selection") == "" {
		query.Set("selection", "default")
	}
	parsed.RawQuery = query.Encode()
	if headers == nil {
		headers = http.Header{}
	} else {
		headers = headers.Clone()
	}
	headers.Set(gateway.DiscoveryServiceAuthorizationHeader, "Bearer backend-identity")
	return f.request(t, method, parsed.String(), headers)
}

func (f *discoveryFixture) assertNoAdmission(t *testing.T) {
	t.Helper()
	assert.Zero(t, f.credentials.gatewayCalls.Load(), "private discovery verified a routing credential")
	assert.Zero(t, f.credentials.workerCalls.Load(), "private discovery authenticated at the worker")
	assert.Zero(t, f.accounting.admissions.Load(), "private discovery persisted an admission")
	assert.Zero(t, f.accounting.attributions.Load(), "private discovery attributed an inference request")
}

type discoveryModel struct{ Model, Provider string }

type discoveryPolicyCatalog struct {
	Default    router.Strategy `json:"default_strategy"`
	Strategies []struct {
		Strategy     router.Strategy
		Available    bool
		Capabilities policy.Capabilities
	}
}

type discoveryRoster struct {
	ReleaseID    string `json:"release_id"`
	PolicySHA256 string `json:"policy_sha256"`
	Clusters     []struct {
		Cluster      string
		Arms, Models []string
	} `json:"clusters"`
}

func discoveryJSON[T any](t *testing.T, response *httptest.ResponseRecorder) T {
	t.Helper()
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var decoded T
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &decoded))
	return decoded
}

func TestPrivateDiscoveryThroughGatewayAndManagedWorker(t *testing.T) {
	for _, target := range []policyregistry.ServingTarget{policyregistry.TargetStable, policyregistry.TargetStaging} {
		t.Run(string(target), func(t *testing.T) {
			f := newDiscoveryFixture(t, target)
			for _, path := range []string{"/v1/router/models", "/v1/router/models?strategy=" + string(router.StrategyHMM)} {
				models := discoveryJSON[struct{ Models []discoveryModel }](t, f.privateRequest(t, http.MethodGet, path, nil))
				assert.ElementsMatch(t, []discoveryModel{{Model: catalog.ModelIDGPT55.String(), Provider: providers.ProviderOpenAI}, {Model: catalog.ModelIDClaudeHaiku45.String(), Provider: providers.ProviderAnthropic}}, models.Models)
			}
			full := discoveryJSON[struct{ Models []discoveryModel }](t, f.privateRequest(t, http.MethodGet, "/v1/router/models?scope=catalog&strategy="+string(router.StrategyHMM), nil))
			assert.Greater(t, len(full.Models), 2)
			assert.Contains(t, full.Models, discoveryModel{Model: catalog.ModelIDClaudeOpus48.String(), Provider: providers.ProviderAnthropic})
			policies := discoveryJSON[discoveryPolicyCatalog](t, f.privateRequest(t, http.MethodGet, "/v1/router/policies", nil))
			assert.Equal(t, router.StrategyHMM, policies.Default)
			var foundHMM bool
			for _, entry := range policies.Strategies {
				if entry.Strategy == router.StrategyHMM {
					foundHMM = true
					assert.True(t, entry.Available)
					assert.Equal(t, policy.SchemaVersionV4, entry.Capabilities.SchemaVersion)
					assert.True(t, entry.Capabilities.HonorsPreferredModels)
					assert.True(t, entry.Capabilities.HonorsClusterModelLists)
					assert.True(t, entry.Capabilities.SupportsRoutingDistribution)
				}
			}
			assert.True(t, foundHMM)
			roster := discoveryJSON[discoveryRoster](t, f.privateRequest(t, http.MethodGet, "/v1/router/hmm-roster?strategy="+string(router.StrategyHMM), nil))
			assert.Equal(t, f.initial.Release.SHA256, roster.ReleaseID)
			assert.Equal(t, f.initialPolicy.SHA256, roster.PolicySHA256)
			require.Len(t, roster.Clusters, 1)
			assert.Equal(t, discoveryClass, roster.Clusters[0].Cluster)
			assert.ElementsMatch(t, []string{discoveryOpenAIArm, discoveryAnthropicArm}, roster.Clusters[0].Arms)
			assert.ElementsMatch(t, []string{catalog.ModelIDGPT55.String(), catalog.ModelIDClaudeHaiku45.String()}, roster.Clusters[0].Models)
			for _, exclusion := range []string{"excluded_models=" + catalog.ModelIDGPT55.String(), "excluded_providers=" + providers.ProviderOpenAI} {
				distribution := discoveryJSON[struct{ Points []cluster.DistributionPoint }](t, f.privateRequest(t, http.MethodGet, "/v1/router/routing-distribution?strategy="+string(router.StrategyHMM)+"&grid=2&"+exclusion, nil))
				require.Len(t, distribution.Points, 2)
				for i, point := range distribution.Points {
					assert.Equal(t, float64(i), point.QualityBias)
					assert.Equal(t, []cluster.ModelShare{{Model: catalog.ModelIDClaudeHaiku45.String(), Share: 1}}, point.Models)
					assert.Positive(t, point.ProjectedCostPer1KInputUSD)
				}
			}
			f.assertNoAdmission(t)
		})
	}
}

func TestPrivateDiscoveryPinsSelectionAcrossActivationAndIgnoresSpoofedHeaders(t *testing.T) {
	f := newDiscoveryFixture(t, policyregistry.TargetStable)
	spoofed, err := policyregistry.EncodeDiscoverySelection(policyregistry.WorkerValidationRequest{Target: policyregistry.TargetStable, Selection: f.next})
	require.NoError(t, err)
	headers := http.Header{}
	headers.Set(policyregistry.DiscoverySelectionHeader, spoofed)
	headers.Set(policyregistry.ServingAssertionHeader, "caller-assertion")
	headers.Set(policyregistry.ServerlessAuthorizationHeader, "Bearer caller-iam")
	headers.Set("X-Weave-Serving-Target", string(policyregistry.TargetInternal))
	headers.Set("Authorization", "Bearer ignored-routing-key")
	headers.Set(auth.RouterKeyHeader, "ignored-dedicated-key")
	headers.Set("X-Api-Key", "ignored-api-key")
	first := discoveryJSON[discoveryRoster](t, f.privateRequest(t, http.MethodGet, "/v1/router/hmm-roster?strategy="+string(router.StrategyHMM), headers))
	assert.Equal(t, f.initial.Release.SHA256, first.ReleaseID)
	f.activateNextOnHop.Store(true)
	inFlight := discoveryJSON[discoveryRoster](t, f.privateRequest(t, http.MethodGet, "/v1/router/hmm-roster?strategy="+string(router.StrategyHMM), nil))
	assert.Equal(t, f.initial.Release.SHA256, inFlight.ReleaseID)
	assert.Equal(t, f.initialPolicy.SHA256, inFlight.PolicySHA256)
	next := discoveryJSON[discoveryRoster](t, f.privateRequest(t, http.MethodGet, "/v1/router/hmm-roster?strategy="+string(router.StrategyHMM), nil))
	assert.Equal(t, f.next.Release.SHA256, next.ReleaseID)
	require.Len(t, next.Clusters, 1)
	assert.Equal(t, []string{discoveryAnthropicArm}, next.Clusters[0].Arms)
	models := discoveryJSON[struct{ Models []discoveryModel }](t, f.privateRequest(t, http.MethodGet, "/v1/router/models?strategy="+string(router.StrategyHMM), nil))
	assert.Equal(t, []discoveryModel{{Model: catalog.ModelIDClaudeHaiku45.String(), Provider: providers.ProviderAnthropic}}, models.Models)
	policies := discoveryJSON[discoveryPolicyCatalog](t, f.privateRequest(t, http.MethodGet, "/v1/router/policies", nil))
	var foundHMM bool
	for _, entry := range policies.Strategies {
		if entry.Strategy == router.StrategyHMM {
			foundHMM = true
			assert.Equal(t, policy.SchemaVersionV4, entry.Capabilities.SchemaVersion)
			assert.False(t, entry.Capabilities.HonorsPreferredModels)
		}
	}
	assert.True(t, foundHMM)
	distribution := discoveryJSON[struct{ Points []cluster.DistributionPoint }](t, f.privateRequest(t, http.MethodGet, "/v1/router/routing-distribution?strategy="+string(router.StrategyHMM)+"&grid=2", nil))
	require.Len(t, distribution.Points, 2)
	for _, point := range distribution.Points {
		assert.Equal(t, []cluster.ModelShare{{Model: catalog.ModelIDClaudeHaiku45.String(), Share: 1}}, point.Models)
	}
	assert.Equal(t, int32(6), f.store.stateReads.Load(), "the worker must not reselect from a mutable head")
	f.assertNoAdmission(t)
}

func TestCredentialedDiscoveryPreservesProfileAndCredentialPrecedence(t *testing.T) {
	f := newDiscoveryFixture(t, policyregistry.TargetStable)
	for _, headers := range []http.Header{
		{auth.RouterKeyHeader: []string{discoveryCredential}, "Authorization": []string{"Bearer rk_invalid"}},
		{"Authorization": []string{"Bearer " + discoveryCredential}},
	} {
		roster := discoveryJSON[discoveryRoster](t, f.request(t, http.MethodGet, "/v1/router/hmm-roster?strategy="+string(router.StrategyHMM), headers))
		assert.Equal(t, f.profile.Release.SHA256, roster.ReleaseID)
		require.Len(t, roster.Clusters, 1)
		assert.Equal(t, []string{discoveryProfileArm}, roster.Clusters[0].Arms)
	}
	assert.Equal(t, int32(2), f.accounting.admissions.Load())
	assert.Equal(t, int32(2), f.accounting.attributions.Load())
	assert.Equal(t, int32(2), f.credentials.workerCalls.Load())
	denied := f.request(t, http.MethodGet, "/v1/router/hmm-roster?strategy="+string(router.StrategyHMM), http.Header{auth.RouterKeyHeader: []string{"rk_invalid"}, "Authorization": []string{"Bearer " + discoveryCredential}})
	assert.Equal(t, http.StatusUnauthorized, denied.Code)
	assert.Equal(t, int32(2), f.workerCalls.Load())
	anonymous := f.request(t, http.MethodGet, "/v1/router/hmm-roster?strategy="+string(router.StrategyHMM), nil)
	assert.Equal(t, http.StatusUnauthorized, anonymous.Code)
	assert.Equal(t, int32(2), f.accounting.admissions.Load())
	assert.Equal(t, int32(2), f.accounting.attributions.Load())
}

func TestDiscoverySelectionCannotAuthorizeOtherRoutes(t *testing.T) {
	f := newDiscoveryFixture(t, policyregistry.TargetStable)
	encoded, err := policyregistry.EncodeDiscoverySelection(policyregistry.WorkerValidationRequest{Target: policyregistry.TargetStable, Selection: f.initial})
	require.NoError(t, err)
	for _, test := range []struct {
		method, path string
		status       int
	}{
		{http.MethodPost, "/v1/messages", http.StatusUnauthorized},
		{http.MethodPost, "/v1/route", http.StatusUnauthorized},
		{http.MethodGet, "/validate", http.StatusUnauthorized},
		{http.MethodGet, "/v1/models", http.StatusUnauthorized},
		{http.MethodPost, "/v1/router/models", http.StatusNotFound},
		{http.MethodHead, "/v1/router/policies", http.StatusNotFound},
		{http.MethodGet, "/v1/router/unknown", http.StatusNotFound},
		{http.MethodGet, "/admin/v1/config", http.StatusNotFound},
		{http.MethodPost, "/internal/v1/router/models", http.StatusNotFound},
		{http.MethodHead, "/internal/v1/router/policies", http.StatusNotFound},
		{http.MethodGet, "/internal/v1/router/unknown", http.StatusNotFound},
	} {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			headers := http.Header{}
			headers.Set(policyregistry.DiscoverySelectionHeader, encoded)
			headers.Set(gateway.DiscoveryServiceAuthorizationHeader, "Bearer backend-identity")
			response := f.request(t, test.method, test.path, headers)
			assert.Equal(t, test.status, response.Code, response.Body.String())
			request := httptest.NewRequest(test.method, test.path, nil)
			request.Header = headers
			direct := httptest.NewRecorder()
			f.worker.ServeHTTP(direct, request)
			assert.Equal(t, test.status, direct.Code, direct.Body.String())
		})
	}
	direct := httptest.NewRecorder()
	f.worker.ServeHTTP(direct, httptest.NewRequest(http.MethodGet, "/v1/router/models", nil))
	assert.Equal(t, http.StatusUnauthorized, direct.Code)
	assert.Zero(t, f.workerCalls.Load())
	assert.Zero(t, f.credentials.workerCalls.Load())
	assert.Zero(t, f.accounting.admissions.Load())
	assert.Zero(t, f.accounting.attributions.Load())
}

func TestManagedDiscoveryKeepsQueryErrors(t *testing.T) {
	f := newDiscoveryFixture(t, policyregistry.TargetStable)
	for _, test := range []struct {
		query  string
		status int
	}{
		{"routing-distribution?strategy=" + string(router.StrategyHMM) + "&grid=1", http.StatusBadRequest},
		{"routing-distribution?strategy=" + string(router.StrategyHMM) + "&grid=102", http.StatusBadRequest},
		{"routing-distribution?strategy=" + string(router.StrategyHMM) + "&grid=invalid", http.StatusBadRequest},
		{"routing-distribution?strategy=" + string(router.StrategyHMM) + "&grid=2&excluded_providers=" + providers.ProviderOpenAI + "," + providers.ProviderAnthropic, http.StatusBadRequest},
		{"routing-distribution?strategy=" + string(router.StrategyHMMBeta) + "&grid=2", http.StatusBadRequest},
		{"hmm-roster?strategy=" + string(router.StrategyHMMBeta), http.StatusServiceUnavailable},
	} {
		response := f.privateRequest(t, http.MethodGet, "/v1/router/"+test.query, nil)
		assert.Equal(t, test.status, response.Code, test.query+": "+response.Body.String())
	}
	f.assertNoAdmission(t)
}

func TestPrivateDiscoveryReadsAssignedProfileWithoutRoutingKey(t *testing.T) {
	f := newDiscoveryFixture(t, policyregistry.TargetStable)
	roster := discoveryJSON[discoveryRoster](t, f.privateRequest(t, http.MethodGet, "/v1/router/hmm-roster?selection=profile&profile_key="+discoveryProfileKey, nil))
	assert.Equal(t, f.profile.Release.SHA256, roster.ReleaseID)
	require.Len(t, roster.Clusters, 1)
	assert.Equal(t, []string{discoveryProfileArm}, roster.Clusters[0].Arms)
	f.assertNoAdmission(t)
}

func TestPrivateDiscoveryRejectsIdentityAndSelectorsBeforeRegistryRead(t *testing.T) {
	for _, test := range []struct {
		name, token, query string
		status             int
	}{
		{"missing identity", "", "selection=default", http.StatusUnauthorized},
		{"wrong identity", "Bearer another-service", "selection=default", http.StatusUnauthorized},
		{"routing key", "Bearer " + discoveryCredential, "selection=default", http.StatusUnauthorized},
		{"missing selection", "Bearer backend-identity", "", http.StatusBadRequest},
		{"default with profile", "Bearer backend-identity", "selection=default&profile_key=" + discoveryProfileKey, http.StatusBadRequest},
		{"invalid profile", "Bearer backend-identity", "selection=profile&profile_key=invalid", http.StatusBadRequest},
		{"duplicate selection", "Bearer backend-identity", "selection=profile&selection=default&profile_key=" + discoveryProfileKey, http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newDiscoveryFixture(t, policyregistry.TargetStable)
			headers := http.Header{}
			headers.Set(gateway.DiscoveryServiceAuthorizationHeader, test.token)
			response := f.request(t, http.MethodGet, "/internal/v1/router/models?"+test.query, headers)
			assert.Equal(t, test.status, response.Code, response.Body.String())
			assert.Zero(t, f.store.stateReads.Load())
			assert.Zero(t, f.workerCalls.Load())
			f.assertNoAdmission(t)
		})
	}
}

func TestPrivateDiscoveryUnavailableProfileNeverFallsBack(t *testing.T) {
	f := newDiscoveryFixture(t, policyregistry.TargetStable)
	response := f.privateRequest(t, http.MethodGet, "/v1/router/models?selection=profile&profile_key="+uuid.NewString(), nil)
	assert.Equal(t, http.StatusNotFound, response.Code)
	assert.Zero(t, f.workerCalls.Load())
	f.assertNoAdmission(t)
}
