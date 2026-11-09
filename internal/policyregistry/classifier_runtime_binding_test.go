package policyregistry_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/policyregistry"
)

const (
	testClassifierWorkerURL      = "https://classifier-aaaaaaaaaaaa---router-classifier-worker-abc123-uc.a.run.app"
	testClassifierWorkerAudience = "https://router-classifier-worker-abc123-uc.a.run.app"
)

func testRuntimeBinding(environment policyregistry.Environment, releaseSHA256 string) map[string]any {
	project := "workweave-prod-01"
	if environment == policyregistry.EnvironmentStaging {
		project = "workweave-staging-01"
	}
	return map[string]any{
		"schema_version":       "router_classifier_runtime_binding_v1",
		"environment":          string(environment),
		"project_id":           project,
		"release_sha256":       releaseSHA256,
		"worker_revision":      "router-classifier-worker-00042-abc",
		"worker_url":           testClassifierWorkerURL,
		"worker_audience":      testClassifierWorkerAudience,
		"image_digest":         "sha256:" + strings.Repeat("6", 64),
		"configuration_sha256": strings.Repeat("5", 64),
		"variants":             map[string]any{"qwen": nil},
	}
}

func encodeRuntimeBinding(t *testing.T, binding map[string]any) []byte {
	t.Helper()
	payload, err := json.Marshal(binding)
	require.NoError(t, err)
	return payload
}

func TestDecodeClassifierRuntimeBindingAcceptsPublishedBinding(t *testing.T) {
	release := strings.Repeat("a", 64)
	binding, err := policyregistry.DecodeClassifierRuntimeBinding(
		encodeRuntimeBinding(t, testRuntimeBinding(policyregistry.EnvironmentProd, release)), policyregistry.EnvironmentProd, release,
	)
	require.NoError(t, err)
	assert.Equal(t, testClassifierWorkerURL, binding.WorkerURL)
	assert.Equal(t, testClassifierWorkerAudience, binding.WorkerAudience)
	assert.Equal(t, "router-classifier-worker-00042-abc", binding.WorkerRevision)
}

func TestDecodeClassifierRuntimeBindingFailsClosed(t *testing.T) {
	release := strings.Repeat("a", 64)
	for name, mutate := range map[string]func(map[string]any){
		"other release":      func(b map[string]any) { b["release_sha256"] = strings.Repeat("b", 64) },
		"other environment":  func(b map[string]any) { b["environment"] = "staging-01"; b["project_id"] = "workweave-staging-01" },
		"other project":      func(b map[string]any) { b["project_id"] = "workweave-staging-01" },
		"unknown schema":     func(b map[string]any) { b["schema_version"] = "router_classifier_runtime_binding_v2" },
		"unknown field":      func(b map[string]any) { b["gateway_url"] = testClassifierWorkerURL },
		"trailing slash url": func(b map[string]any) { b["worker_url"] = testClassifierWorkerURL + "/" },
		"url with path":      func(b map[string]any) { b["worker_url"] = testClassifierWorkerURL + "/v5" },
		"url with port": func(b map[string]any) {
			b["worker_url"] = strings.Replace(testClassifierWorkerURL, ".app", ".app:443", 1)
		},
		"http url": func(b map[string]any) { b["worker_url"] = strings.Replace(testClassifierWorkerURL, "https", "http", 1) },
		"uppercase host": func(b map[string]any) {
			b["worker_url"] = "https://" + strings.ToUpper(strings.TrimPrefix(testClassifierWorkerURL, "https://"))
		},
		"longer service name":       func(b map[string]any) { b["worker_url"] = "https://router-classifier-worker-evil-abc123-uc.a.run.app" },
		"gateway url":               func(b map[string]any) { b["worker_url"] = "https://router-classifier-gateway-abc123-uc.a.run.app" },
		"non cloud run audience":    func(b map[string]any) { b["worker_audience"] = "https://router-classifier-worker.example.com" },
		"missing worker url":        func(b map[string]any) { delete(b, "worker_url") },
		"whitespace revision":       func(b map[string]any) { b["worker_revision"] = "router classifier" },
		"unprefixed image digest":   func(b map[string]any) { b["image_digest"] = strings.Repeat("6", 64) },
		"invalid configuration sha": func(b map[string]any) { b["configuration_sha256"] = "configuration" },
		"non object variants":       func(b map[string]any) { b["variants"] = []string{"qwen"} },
	} {
		t.Run(name, func(t *testing.T) {
			binding := testRuntimeBinding(policyregistry.EnvironmentProd, release)
			mutate(binding)
			_, err := policyregistry.DecodeClassifierRuntimeBinding(encodeRuntimeBinding(t, binding), policyregistry.EnvironmentProd, release)
			require.Error(t, err)
		})
	}
}

func TestClassifierRuntimeBindingURIMatchesPublisherLayout(t *testing.T) {
	release := strings.Repeat("a", 64)
	assert.Equal(t,
		"gs://weave_ml/weave_registry/router/serving/runtime_bindings/staging-01/classifier_releases/"+release+".json",
		policyregistry.ClassifierRuntimeBindingURI("gs://weave_ml/weave_registry/router/serving", policyregistry.EnvironmentStaging, release),
	)
}
