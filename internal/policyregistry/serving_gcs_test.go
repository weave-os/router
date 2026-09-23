package policyregistry_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"cloud.google.com/go/storage"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/option"

	"weave-os/router/internal/policyregistry"
)

type servingGCSFixture struct {
	mu         sync.Mutex
	objects    map[string]map[int64][]byte
	current    map[string]int64
	generation int64
	exactReads int
}

func (f *servingGCSFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodPost && r.URL.Query().Get("uploadType") == "multipart" {
		_, parameters, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		reader := multipart.NewReader(r.Body, parameters["boundary"])
		metadata, err := reader.NextPart()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var object struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(metadata).Decode(&object); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		media, err := reader.NextPart()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		payload, err := io.ReadAll(media)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		expected, err := strconv.ParseInt(r.URL.Query().Get("ifGenerationMatch"), 10, 64)
		if err != nil || f.current[object.Name] != expected {
			w.WriteHeader(http.StatusPreconditionFailed)
			_, _ = io.WriteString(w, `{"error":{"code":412,"message":"generation conflict"}}`)
			return
		}
		f.generation++
		if f.objects[object.Name] == nil {
			f.objects[object.Name] = make(map[int64][]byte)
		}
		f.objects[object.Name][f.generation] = payload
		f.current[object.Name] = f.generation
		f.writeAttrs(w, object.Name, f.generation)
		return
	}
	if r.Method == http.MethodGet {
		_, name, found := strings.Cut(r.URL.Path, "/o/")
		if !found || f.current[name] == 0 {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"code":404,"message":"not found"}}`)
			return
		}
		generation := f.current[name]
		if raw := r.URL.Query().Get("generation"); raw != "" {
			var err error
			generation, err = strconv.ParseInt(raw, 10, 64)
			if err != nil || f.objects[name][generation] == nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
		}
		if r.URL.Query().Get("alt") == "media" {
			if r.URL.Query().Get("generation") == "" {
				http.Error(w, "media read omitted exact generation", http.StatusBadRequest)
				return
			}
			f.exactReads++
			w.Header().Set("X-Goog-Generation", strconv.FormatInt(generation, 10))
			w.Header().Set("Content-Length", strconv.Itoa(len(f.objects[name][generation])))
			_, _ = w.Write(f.objects[name][generation])
			return
		}
		f.writeAttrs(w, name, generation)
		return
	}
	http.Error(w, "unsupported fixture request: "+r.Method+" "+r.URL.String(), http.StatusBadRequest)
}

func (f *servingGCSFixture) writeAttrs(w http.ResponseWriter, name string, generation int64) {
	_ = json.NewEncoder(w).Encode(map[string]any{"name": name, "bucket": "registry-test", "generation": strconv.FormatInt(generation, 10), "metageneration": "1", "size": strconv.Itoa(len(f.objects[name][generation])), "contentType": "application/json"})
}

func newServingGCSFixture(t *testing.T) (*policyregistry.Registry, *servingGCSFixture) {
	t.Helper()
	fixture := &servingGCSFixture{objects: make(map[string]map[int64][]byte), current: make(map[string]int64)}
	server := httptest.NewServer(fixture)
	t.Cleanup(server.Close)
	client, err := storage.NewClient(context.Background(), option.WithEndpoint(server.URL), option.WithoutAuthentication(), storage.WithJSONReads())
	require.NoError(t, err)
	registry, err := policyregistry.NewRegistry(client, testRegistryRoot)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, registry.Close()) })
	return registry, fixture
}

func TestGCSManagedServingImmutablePublicationAndDigestTampering(t *testing.T) {
	registry, fixture := newServingGCSFixture(t)
	ctx := context.Background()
	payload, err := policyregistry.CanonicalBytes(fixtureSet("published"))
	require.NoError(t, err)
	ref, err := registry.PublishServingManifest(ctx, policyregistry.ServingSelectionSets, payload)
	require.NoError(t, err)
	republished, err := registry.PublishServingManifest(ctx, policyregistry.ServingSelectionSets, payload)
	require.NoError(t, err)
	require.Equal(t, ref, republished)
	resolved, err := registry.ServingRef(ctx, policyregistry.ServingSelectionSets, ref.SHA256)
	require.NoError(t, err)
	require.Equal(t, ref, resolved)
	_, storedPayload, err := registry.ReadServingObject(ctx, policyregistry.ServingSelectionSets, ref)
	require.NoError(t, err)
	require.Equal(t, payload, storedPayload)
	wrongGeneration := ref
	wrongGeneration.Generation++
	_, _, err = registry.ReadServingObject(ctx, policyregistry.ServingSelectionSets, wrongGeneration)
	require.ErrorIs(t, err, policyregistry.ErrNotFound)
	fixture.mu.Lock()
	for name, generations := range fixture.objects {
		if strings.HasSuffix(name, ref.SHA256+".json") {
			generations[ref.Generation] = []byte(`{"tampered":true}`)
		}
	}
	fixture.mu.Unlock()
	_, _, err = registry.ReadServingObject(ctx, policyregistry.ServingSelectionSets, ref)
	require.ErrorContains(t, err, "digest mismatch")
	_, err = registry.PublishServingManifest(ctx, policyregistry.ServingSelectionSets, payload)
	require.ErrorContains(t, err, "different bytes")
}

func TestGCSManagedServingPublishRequiresCanonicalBytesWhileStoredReadsTolerateDrift(t *testing.T) {
	registry, fixture := newServingGCSFixture(t)
	ctx := context.Background()
	set := fixtureSet("drifted")
	canonical, err := policyregistry.CanonicalBytes(set)
	require.NoError(t, err)
	drifted := driftedPayload(t, canonical)

	_, err = registry.PublishServingManifest(ctx, policyregistry.ServingSelectionSets, drifted)
	require.ErrorContains(t, err, "canonical", "publish must not regress to accepting drifted bytes")

	name := "weave_registry/router_serving/v1/selection_sets/sha256/" + policyregistry.Digest(drifted) + ".json"
	fixture.mu.Lock()
	fixture.objects[name] = map[int64][]byte{1: drifted}
	fixture.current[name] = 1
	fixture.mu.Unlock()
	ref := policyregistry.ObjectRef{URI: testRegistryRoot + "/router_serving/v1/selection_sets/sha256/" + policyregistry.Digest(drifted) + ".json", SHA256: policyregistry.Digest(drifted), Generation: 1}
	manifest, payload, err := registry.ReadServingObject(ctx, policyregistry.ServingSelectionSets, ref)
	require.NoError(t, err)
	require.Equal(t, drifted, payload)
	require.Equal(t, &set, manifest)
}

func TestGCSManagedServingConcurrentRegistrationReturnsOneImmutableReference(t *testing.T) {
	registry, _ := newServingGCSFixture(t)
	payload, err := policyregistry.CanonicalBytes(fixtureSet("concurrent"))
	require.NoError(t, err)
	const registrations = 8
	refs := make(chan policyregistry.ObjectRef, registrations)
	errs := make(chan error, registrations)
	for range registrations {
		go func() {
			ref, err := registry.PublishServingManifest(context.Background(), policyregistry.ServingSelectionSets, payload)
			refs <- ref
			errs <- err
		}()
	}
	var expected policyregistry.ObjectRef
	for range registrations {
		require.NoError(t, <-errs)
		ref := <-refs
		if expected == (policyregistry.ObjectRef{}) {
			expected = ref
		}
		require.Equal(t, expected, ref)
	}
	require.Equal(t, policyregistry.Digest(payload), expected.SHA256)
	require.NoError(t, policyregistry.ValidateServingRef(expected, testRegistryRoot, policyregistry.ServingSelectionSets))
}

func TestGCSManagedServingGenerationCASAndAuthoritativeExactRead(t *testing.T) {
	registry, fixture := newServingGCSFixture(t)
	ctx := context.Background()
	_, err := registry.ReadServingState(ctx, policyregistry.TargetStable)
	require.ErrorIs(t, err, policyregistry.ErrNotFound)
	set := fixtureSet("active")
	proposal := fixtureProposal(t, policyregistry.ServingStateSnapshot{}, set, servingEpoch)
	transition, err := policyregistry.NextServingActivation(policyregistry.ServingStateSnapshot{}, servingPayload(t, proposal), servingRef(t, policyregistry.ServingProposals, proposal), testRegistryRoot, "workflow", servingEpoch)
	require.NoError(t, err)
	first, err := registry.CompareAndSwapServingState(ctx, transition.Snapshot.State, 0)
	require.NoError(t, err)
	require.Positive(t, first.Generation)
	observed, err := registry.ReadServingState(ctx, policyregistry.TargetStable)
	require.NoError(t, err)
	require.Equal(t, first, observed)
	_, err = registry.CompareAndSwapServingState(ctx, transition.Snapshot.State, 0)
	require.ErrorIs(t, err, policyregistry.ErrConflict)
	proposal = fixtureProposal(t, first, set, servingEpoch)
	transition, err = policyregistry.NextServingActivation(first, servingPayload(t, proposal), servingRef(t, policyregistry.ServingProposals, proposal), testRegistryRoot, "workflow", servingEpoch)
	require.NoError(t, err)
	second, err := registry.CompareAndSwapServingState(ctx, transition.Snapshot.State, first.Generation)
	require.NoError(t, err)
	require.Greater(t, second.Generation, first.Generation)
	_, err = registry.CompareAndSwapServingState(ctx, transition.Snapshot.State, first.Generation)
	require.ErrorIs(t, err, policyregistry.ErrConflict)
	observed, err = registry.ReadServingState(ctx, policyregistry.TargetStable)
	require.NoError(t, err)
	require.Equal(t, second, observed)
	fixture.mu.Lock()
	require.GreaterOrEqual(t, fixture.exactReads, 2, fmt.Sprintf("reads: %d", fixture.exactReads))
	fixture.mu.Unlock()
}

func TestGCSManagedServingVerifiesExactEvidenceBytesAndGeneration(t *testing.T) {
	registry, fixture := newServingGCSFixture(t)
	ref := artifactRef("evidence")
	_, name, _ := strings.Cut(strings.TrimPrefix(ref.URI, "gs://"), "/")
	fixture.objects[name] = map[int64][]byte{ref.Generation: []byte("evidence")}
	fixture.current[name] = ref.Generation
	require.NoError(t, registry.VerifyServingArtifact(context.Background(), ref))
	wrongGeneration := ref
	wrongGeneration.Generation++
	require.ErrorIs(t, registry.VerifyServingArtifact(context.Background(), wrongGeneration), policyregistry.ErrNotFound)
	fixture.mu.Lock()
	fixture.objects[name][ref.Generation] = []byte("tampered")
	fixture.mu.Unlock()
	require.ErrorContains(t, registry.VerifyServingArtifact(context.Background(), ref), "digest mismatch")
	require.ErrorIs(t, registry.VerifyServingArtifact(context.Background(), artifactRef("missing-evidence")), policyregistry.ErrNotFound)
}
