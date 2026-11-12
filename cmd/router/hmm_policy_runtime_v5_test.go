package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
)

func v5WorkerCandidate(workerURL string) policyregistry.Candidate {
	return policyregistry.Candidate{
		ClassifierReleaseV5:      &policyregistry.ClassifierReleaseManifestV1{APIMajor: policy.ClassifierAPIMajorV5, WireSchema: policy.SchemaVersionV5},
		SupportedAPIMajors:       []policy.ClassifierAPIMajor{policy.ClassifierAPIMajorV5},
		ClassifierReleaseID:      strings.Repeat("a", 64),
		ClassifierSelectionID:    strings.Repeat("b", 64),
		ClassifierWorkerURL:      workerURL,
		ClassifierWorkerAudience: workerURL,
	}
}

func TestHMMPolicySnapshotBuilderV5CallsRuntimeBindingWorker(t *testing.T) {
	var mu sync.Mutex
	var capabilityQueries []url.Values
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v5/capabilities" {
			mu.Lock()
			capabilityQueries = append(capabilityQueries, request.URL.Query())
			mu.Unlock()
		}
		http.NotFound(w, request)
	}))
	defer worker.Close()
	candidate := v5WorkerCandidate(worker.URL)
	build := hmmPolicySnapshotBuilder(nil, []router.Strategy{router.StrategyHMM}, policySidecarAuthNone, time.Second, time.Second)

	_, err := build(context.Background(), candidate)
	require.ErrorContains(t, err, "read v5 classifier capabilities")
	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, capabilityQueries, "the v5 builder must probe the runtime binding's worker URL")
	assert.Equal(t, candidate.ClassifierReleaseID, capabilityQueries[0].Get("classifier_release_id"))
	assert.Equal(t, candidate.ClassifierSelectionID, capabilityQueries[0].Get("classifier_selection_id"))
}

func TestHMMPolicySnapshotBuilderV5RequiresRuntimeBindingWorker(t *testing.T) {
	build := hmmPolicySnapshotBuilder(nil, []router.Strategy{router.StrategyHMM}, policySidecarAuthNone, time.Second, time.Second)
	_, err := build(context.Background(), v5WorkerCandidate(""))
	require.ErrorContains(t, err, "no worker URL or audience")
}
