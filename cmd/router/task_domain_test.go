package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"cloud.google.com/go/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/option"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/router/hmm/rosterdata"
	"weave-os/router/internal/router/taskdomain"
)

func TestTaskBindingRequiresAdmittedAndLoadedRelease(t *testing.T) {
	var runtime *taskDomainRuntime
	resolver, evidence, digest, err := runtime.bind(policyregistry.Candidate{})
	require.NoError(t, err)
	assert.Nil(t, resolver)
	assert.Nil(t, evidence)
	assert.Empty(t, digest)
	releaseSHA := strings.Repeat("a", 64)
	candidate := policyregistry.Candidate{AuxiliaryModels: map[string]policyregistry.ObjectRef{taskdomain.AuxiliaryModel: {SHA256: releaseSHA}}, Policy: &rosterdata.Roster{SHA256: strings.Repeat("b", 64)}}
	_, _, _, err = runtime.bind(candidate)
	require.ErrorContains(t, err, "not loaded")
	runtime = &taskDomainRuntime{bindings: map[string]loadedTaskDomain{strings.Repeat("c", 64): {}}}
	_, _, _, err = runtime.bind(candidate)
	require.ErrorContains(t, err, "unavailable")
	runtime.bindings[releaseSHA] = loadedTaskDomain{release: taskdomain.Release{Evidence: map[string]string{strings.Repeat("d", 64): strings.Repeat("e", 64)}}}
	resolver, evidence, digest, err = runtime.bind(candidate)
	require.NoError(t, err)
	require.NotNil(t, resolver)
	assert.Nil(t, evidence)
	assert.Empty(t, digest)
	outcome := resolver.Resolve(context.Background(), taskdomain.Input{UserText: "Synthetic task"})
	assert.Equal(t, taskdomain.EvidenceUnavailable, outcome.Status)
	assert.Equal(t, releaseSHA, outcome.ReleaseSHA256)
	loaded := runtime.bindings[releaseSHA]
	loaded.release.Evidence[candidate.Policy.SHA256] = strings.Repeat("f", 64)
	loaded.evidence = map[string][]byte{strings.Repeat("f", 64): []byte(`{"invalid":"evidence"}`)}
	runtime.bindings[releaseSHA] = loaded
	_, _, _, err = runtime.bind(candidate)
	require.Error(t, err, "invalid admitted evidence cannot become a different release")
}

func TestTaskDomainBindingDigestRequiresExactBytes(t *testing.T) {
	payload := []byte("[]")
	digest := rosterdata.SHA256Hex(payload)
	path := filepath.Join(t.TempDir(), "bindings.json")
	require.NoError(t, os.WriteFile(path, payload, 0600))
	reader := &taskDomainMetadataReader{}
	for _, expected := range []string{digest, ""} {
		read, err := reader.read(context.Background(), path, expected, taskDomainBindings)
		require.NoError(t, err)
		assert.Equal(t, payload, read)
	}
	for _, expected := range []string{strings.Repeat("a", 64), "invalid"} {
		_, err := reader.read(context.Background(), path, expected, taskDomainBindings)
		require.ErrorContains(t, err, "digest mismatch")
	}
}

func TestTaskDomainGCSReferenceRejectsUnreviewedObjects(t *testing.T) {
	digest := strings.Repeat("a", 64)
	valid := taskDomainTestURI(taskDomainBindings, digest)
	for _, reference := range []string{
		strings.Replace(valid, "gs://", "https://", 1),
		strings.Replace(valid, "weave_ml", "other-bucket", 1),
		strings.Replace(valid, "/bindings/", "/releases/", 1),
		strings.Replace(valid, digest, strings.Repeat("b", 64), 1),
		strings.Replace(valid, "/sha256/", "/sha256/../sha256/", 1),
		strings.Replace(valid, ".json#", ".json?alt=media#", 1),
		strings.Replace(valid, "/bindings/", "/%62indings/", 1),
		strings.Replace(valid, "#7", "", 1),
		strings.Replace(valid, "#7", "#0", 1),
		strings.Replace(valid, "#7", "#-1", 1),
		strings.Replace(valid, "#7", "#07", 1),
		strings.Replace(valid, "#7", "#9223372036854775808", 1),
	} {
		t.Run(reference, func(t *testing.T) {
			_, _, err := taskDomainGCSReference(reference, digest, taskDomainBindings)
			require.Error(t, err)
		})
	}
	_, _, err := taskDomainGCSReference(valid, "", taskDomainBindings)
	require.Error(t, err, "GCS inventory requires a separately reviewed digest")
}

func TestTaskDomainRuntimeConfigurationRemainsOptional(t *testing.T) {
	t.Setenv("ROUTER_TASK_DOMAIN_BINDINGS_PATH", "")
	t.Setenv("ROUTER_TASK_DOMAIN_BINDINGS_SHA256", "")
	runtime, err := loadTaskDomainRuntime(nil)
	require.NoError(t, err)
	assert.Nil(t, runtime)
	t.Setenv("ROUTER_TASK_DOMAIN_BINDINGS_SHA256", strings.Repeat("a", 64))
	_, err = loadTaskDomainRuntime(nil)
	require.ErrorContains(t, err, "requires a bindings path")
	t.Setenv("ROUTER_TASK_DOMAIN_BINDINGS_PATH", "/synthetic/bindings.json")
	_, err = loadTaskDomainRuntime(nil)
	require.ErrorContains(t, err, "persistent router storage")
}

func TestTaskDomainGCSBindingLoadsExactGenerationAndBytes(t *testing.T) {
	evidence := []byte(`{"synthetic":"evidence"}`)
	evidenceDigest := rosterdata.SHA256Hex(evidence)
	policyDigest := strings.Repeat("a", 64)
	release := taskdomain.Release{
		SchemaVersion: taskdomain.SchemaVersion, ProjectionVersion: taskdomain.ProjectionVersion,
		PromptSHA256: rosterdata.SHA256Hex([]byte(taskdomain.SystemPrompt)),
		Files:        map[string]string{}, Evidence: map[string]string{policyDigest: evidenceDigest},
	}
	for _, name := range []string{"model.safetensors", "config.json", "tokenizer.json", "tokenizer_config.json", "generation_config.json"} {
		release.Files[name] = strings.Repeat("b", 64)
	}
	releasePayload, err := json.Marshal(release)
	require.NoError(t, err)
	releaseDigest := rosterdata.SHA256Hex(releasePayload)
	binding := taskDomainBinding{
		ReleaseFile: taskDomainTestURI(taskDomainReleases, releaseDigest), ReleaseSHA256: releaseDigest,
		Endpoint: "https://classifier.example.net", BearerEnv: "TEST_TASK_DOMAIN_BEARER",
		EvidenceFiles: map[string]string{evidenceDigest: taskDomainTestURI(taskDomainEvidence, evidenceDigest)},
	}
	t.Setenv(binding.BearerEnv, strings.Repeat("x", 32))
	bindingsPayload, err := json.Marshal([]taskDomainBinding{binding})
	require.NoError(t, err)
	bindingsDigest := rosterdata.SHA256Hex(bindingsPayload)
	objects := map[string][]byte{
		taskDomainTestObject(taskDomainBindings, bindingsDigest): bindingsPayload,
		taskDomainTestObject(taskDomainReleases, releaseDigest):  releasePayload,
		taskDomainTestObject(taskDomainEvidence, evidenceDigest): evidence,
	}
	reader := taskDomainTestGCSReader(t, objects)
	loaded, err := loadTaskDomainBindings(context.Background(), reader, taskDomainTestURI(taskDomainBindings, bindingsDigest), bindingsDigest)
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	assert.Equal(t, release, loaded[releaseDigest].release)
	assert.Equal(t, evidence, loaded[releaseDigest].evidence[evidenceDigest])
	require.NotNil(t, loaded[releaseDigest].classifier)

	for _, object := range []string{taskDomainTestObject(taskDomainBindings, bindingsDigest), taskDomainTestObject(taskDomainReleases, releaseDigest), taskDomainTestObject(taskDomainEvidence, evidenceDigest)} {
		t.Run(object, func(t *testing.T) {
			original := objects[object]
			objects[object] = append(append([]byte(nil), original...), '\n')
			_, err := loadTaskDomainBindings(context.Background(), reader, taskDomainTestURI(taskDomainBindings, bindingsDigest), bindingsDigest)
			require.ErrorContains(t, err, "digest mismatch")
			objects[object] = original
		})
	}
	_, err = reader.read(context.Background(), strings.Replace(taskDomainTestURI(taskDomainBindings, bindingsDigest), "#7", "#8", 1), bindingsDigest, taskDomainBindings)
	require.Error(t, err, "a missing generation must not fall back to the latest object")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = reader.read(ctx, taskDomainTestURI(taskDomainBindings, bindingsDigest), bindingsDigest, taskDomainBindings)
	require.ErrorIs(t, err, context.Canceled)
	for _, count := range []int{2, 17} {
		repeated := make([]taskDomainBinding, count)
		for index := range repeated {
			repeated[index] = binding
		}
		payload, err := json.Marshal(repeated)
		require.NoError(t, err)
		digest := rosterdata.SHA256Hex(payload)
		objects[taskDomainTestObject(taskDomainBindings, digest)] = payload
		_, err = loadTaskDomainBindings(context.Background(), reader, taskDomainTestURI(taskDomainBindings, digest), digest)
		if count == 2 {
			require.ErrorContains(t, err, "duplicate")
		} else {
			require.ErrorContains(t, err, "invalid task classifier binding inventory")
		}
	}
}

func TestTaskDomainMetadataAndInventoryBounds(t *testing.T) {
	reader := &taskDomainMetadataReader{}
	for _, payload := range []string{"[]", "null", "[{}] {}", `[{"unknown":true}]`} {
		path := filepath.Join(t.TempDir(), "bindings.json")
		require.NoError(t, os.WriteFile(path, []byte(payload), 0600))
		_, err := loadTaskDomainBindings(context.Background(), reader, path, "")
		require.Error(t, err)
	}
	oversized := []byte(strings.Repeat("x", maxTaskDomainMetadataBytes+1))
	path := filepath.Join(t.TempDir(), "bindings.json")
	require.NoError(t, os.WriteFile(path, oversized, 0600))
	_, err := reader.read(context.Background(), path, "", taskDomainBindings)
	require.ErrorContains(t, err, "size limit")
	digest := rosterdata.SHA256Hex(oversized)
	gcsReader := taskDomainTestGCSReader(t, map[string][]byte{taskDomainTestObject(taskDomainBindings, digest): oversized})
	_, err = gcsReader.read(context.Background(), taskDomainTestURI(taskDomainBindings, digest), digest, taskDomainBindings)
	require.ErrorContains(t, err, "size limit")
}

func taskDomainTestObject(kind taskDomainMetadataKind, digest string) string {
	return taskDomainRegistryPrefix + string(kind) + "/sha256/" + digest + ".json"
}

func taskDomainTestURI(kind taskDomainMetadataKind, digest string) string {
	return "gs://weave_ml/" + taskDomainTestObject(kind, digest) + "#7"
}

func taskDomainTestGCSReader(t *testing.T, objects map[string][]byte) *taskDomainMetadataReader {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, name, found := strings.Cut(r.URL.Path, "/b/weave_ml/o/")
		payload, exists := objects[name]
		if r.Method != http.MethodGet || !found || !exists || r.URL.Query().Get("generation") != "7" || r.URL.Query().Get("alt") != "media" {
			http.Error(w, "immutable object not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		w.Header().Set("X-Goog-Generation", "7")
		_, _ = io.Copy(w, strings.NewReader(string(payload)))
	}))
	t.Cleanup(server.Close)
	client, err := storage.NewClient(context.Background(), option.WithEndpoint(server.URL), option.WithoutAuthentication(), storage.WithJSONReads())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	return &taskDomainMetadataReader{client: client}
}
