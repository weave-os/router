package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/policyregistry"
	"weave-os/router/smoke/hmmstub"
)

func TestServingExportSelfHostedRequiresContractFlagsBeforeOpeningRegistry(t *testing.T) {
	opened := false
	open := func(context.Context, string) (policyregistry.SelfHostedExportStore, func(), error) {
		opened = true
		return nil, nil, errors.New("unexpected registry open")
	}
	err := runServingWith(context.Background(), []string{string(commandExportSelfHosted), "--target", "prod/stable", "--out", t.TempDir()}, servingDependencies{openExportSource: open, writeOutput: writeJSON})
	require.ErrorContains(t, err, "requires --target, --out, --router-image-digest and --classifier-url")
	require.False(t, opened)
}

func TestServingExportSelfHostedDefaultsToTheServingRegistryRoot(t *testing.T) {
	var openedRoot string
	open := func(_ context.Context, root string) (policyregistry.SelfHostedExportStore, func(), error) {
		openedRoot = root
		return nil, nil, errors.New("stop after open")
	}
	err := runServingWith(context.Background(), []string{
		string(commandExportSelfHosted), "--target", "prod/stable", "--out", t.TempDir(),
		"--router-image-digest", "sha256:" + strings.Repeat("c", 64), "--classifier-url", "http://hmm-sidecar:8080",
	}, servingDependencies{openExportSource: open, writeOutput: writeJSON})
	require.ErrorContains(t, err, "stop after open")
	require.Equal(t, "gs://weave_ml/weave_registry/router/serving", openedRoot)
}

func TestServingExportSelfHostedPrintsTheContractDocument(t *testing.T) {
	graph, err := hmmstub.NewServingGraph("https://hmm-stub.invalid")
	require.NoError(t, err)
	source := t.TempDir()
	require.NoError(t, graph.Write(source))
	open := func(context.Context, string) (policyregistry.SelfHostedExportStore, func(), error) {
		store, err := policyregistry.NewDirectoryServingStore(source)
		if err != nil {
			return nil, nil, err
		}
		return directoryExportSource{store, source}, func() {}, nil
	}
	var printed any
	err = runServingWith(context.Background(), []string{
		string(commandExportSelfHosted), "--target", string(hmmstub.ServingTarget), "--out", t.TempDir(),
		"--router-image-digest", hmmstub.ServingImageDigest, "--classifier-url", "http://hmm-sidecar:8080",
	}, servingDependencies{openExportSource: open, writeOutput: func(value any) error { printed = value; return nil }})
	require.NoError(t, err)
	payload, err := json.Marshal(printed)
	require.NoError(t, err)
	var document map[string]map[string]any
	require.NoError(t, json.Unmarshal(payload, &document))
	require.ElementsMatch(t, []string{"router_env", "classifier", "classifier_env", "source"}, keys(document))
	require.ElementsMatch(t, []string{
		"ROUTER_SERVING_TARGET", "ROUTER_SERVING_PROJECT", "ROUTER_SERVING_REGION", "ROUTER_SERVING_REVISION", "ROUTER_SERVING_IMAGE_DIGEST",
		"ROUTER_SERVING_CONFIGURATION_URI", "ROUTER_SERVING_CONFIGURATION_SHA256", "ROUTER_SERVING_CONFIGURATION_GENERATION",
		"ROUTER_SERVING_SELECTION_SET_URI", "ROUTER_SERVING_SELECTION_SET_SHA256", "ROUTER_SERVING_SELECTION_SET_GENERATION",
	}, keys(document["router_env"]))
	require.ElementsMatch(t, []string{"image_digest", "package_uri", "package_sha256", "release_manifest_path", "selection_path", "rebound"}, keys(document["classifier"]))
	require.Equal(t, false, document["classifier"]["rebound"])
	require.Equal(t, map[string]any{
		"CLASSIFIER_LOCAL_RELEASE_MANIFEST_PATH": "classifier/release.json",
		"CLASSIFIER_LOCAL_SELECTION_PATH":        "classifier/selection.json",
	}, document["classifier_env"])
	require.ElementsMatch(t, []string{"target", "activation_id", "selection_set_sha256"}, keys(document["source"]))
	require.Equal(t, "selfhosted", document["router_env"]["ROUTER_SERVING_PROJECT"])
	require.Equal(t, "local", document["router_env"]["ROUTER_SERVING_REGION"])
	require.Equal(t, "selfhosted-router", document["router_env"]["ROUTER_SERVING_REVISION"])
}

type directoryExportSource struct {
	*policyregistry.DirectoryServingStore
	dir string
}

func (s directoryExportSource) ReadPolicyPayload(_ context.Context, ref policyregistry.ObjectRef) ([]byte, error) {
	return policyregistry.ReadDirectoryPolicyPayload(s.dir, s.RootURI(), ref)
}

func keys[V any](values map[string]V) []string {
	out := make([]string, 0, len(values))
	for key := range values {
		out = append(out, key)
	}
	return out
}
