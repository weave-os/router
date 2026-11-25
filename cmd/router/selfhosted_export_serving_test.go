package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/server"
	"weave-os/router/smoke/hmmstub"
)

func TestSelfHostedExportPreparesTheRouterAgainstALocalClassifier(t *testing.T) {
	var capabilitySelections []string
	stub := hmmstub.Handler()
	classifier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v5/capabilities" {
			capabilitySelections = append(capabilitySelections, request.URL.Query().Get("classifier_selection_id"))
		}
		stub.ServeHTTP(w, request)
	}))
	t.Cleanup(classifier.Close)
	source, err := hmmstub.NewServingGraph("https://hmm-stub.invalid")
	require.NoError(t, err)
	sourceDir := t.TempDir()
	require.NoError(t, source.Write(sourceDir))
	sourceStore, err := policyregistry.NewDirectoryServingStore(sourceDir)
	require.NoError(t, err)

	exportDir := t.TempDir()
	exported, err := policyregistry.ExportSelfHostedServingGraph(context.Background(), directoryExportSource{sourceStore, sourceDir}, policyregistry.SelfHostedExportRequest{
		Target: hmmstub.ServingTarget,
		Worker: policyregistry.WorkerIdentity{
			Target: hmmstub.ServingTarget, Project: policyregistry.SelfHostedServingProject, Region: policyregistry.SelfHostedServingRegion,
			Revision: policyregistry.SelfHostedServingRevision, ImageDigest: hmmstub.ServingImageDigest,
		},
		ClassifierURL: classifier.URL,
		Dir:           exportDir,
	})
	require.NoError(t, err)

	t.Setenv("ROUTER_SERVING_REGISTRY_URI", exportDir)
	store, closeStore, err := openServingStore(context.Background(), server.DeploymentModeSelfHosted, exportDir)
	require.NoError(t, err)
	t.Cleanup(closeStore)
	cache, err := policyregistry.NewServingRuntimeCache(store, hmmPolicySnapshotBuilder(
		map[string]struct{}{providers.ProviderOpenAI: {}}, managedServingStrategies, policySidecarAuthNone, time.Second, time.Second,
	))
	require.NoError(t, err)
	env := exported.RouterEnv
	identity := policyregistry.WorkerIdentity{
		Target: env.Target, Project: env.Project, Region: env.Region, Revision: env.Revision, ImageDigest: env.ImageDigest,
		Configuration: policyregistry.ObjectRef{URI: env.ConfigurationURI, SHA256: env.ConfigurationSHA256, Generation: 1},
	}
	selection := policyregistry.ObjectRef{URI: env.SelectionSetURI, SHA256: env.SelectionSetSHA256, Generation: 1}
	baseline, err := cache.PrepareWorker(context.Background(), identity, selection)
	require.NoError(t, err)
	require.NotNil(t, baseline.Policy)
	selectionBytes, err := os.ReadFile(filepath.Join(exportDir, exported.ClassifierEnv.SelectionPath))
	require.NoError(t, err)
	var selectionSet policyregistry.SelectionSetV4
	require.NoError(t, policyregistry.StrictDecode(selectionBytes, &selectionSet))
	_, err = cache.Snapshot(context.Background(), policyregistry.SessionReleaseBinding{
		Target: hmmstub.ServingTarget, Selection: policyregistry.ServingSelection{Release: selectionSet.Code.Candidate, Binding: selection},
	})
	require.NoError(t, err)
	require.NotEmpty(t, capabilitySelections)
	for _, selectionID := range capabilitySelections {
		require.Equal(t, env.SelectionSetSHA256, selectionID)
	}

	servingSource := exportedServingSource(identity)
	require.NotNil(t, servingSource)
	require.Equal(t, string(exported.Source.Target), servingSource.Target)
	require.Equal(t, exported.Source.ActivationID, servingSource.ActivationID)
	require.Equal(t, exported.Source.SelectionSetSHA256, servingSource.SelectionSetSHA256)
	require.Contains(t, managedServingStrategies, router.StrategyHMMEmbedding)
}

func TestExportedServingSourceIsAbsentForManagedRegistries(t *testing.T) {
	t.Setenv("ROUTER_SERVING_REGISTRY_URI", "gs://weave_ml/weave_registry/router/serving")
	require.Nil(t, exportedServingSource(policyregistry.WorkerIdentity{}))
}

func TestUIEnabledParsesBooleansAndDefaultsOnForAStandaloneRouter(t *testing.T) {
	t.Setenv("ROUTER_INTERNAL_SERVICE_TOKEN", "")
	for value, enabled := range map[string]bool{"": true, "true": true, "TRUE": true, "1": true, "false": false, " False ": false, "0": false, "not-a-bool": true} {
		t.Setenv("ROUTER_UI_ENABLED", value)
		require.Equal(t, enabled, uiEnabledFromEnv(), value)
	}
}

func TestUIDefaultsOffForASelfHostedRouterLinkedToWeave(t *testing.T) {
	t.Setenv("ROUTER_UI_ENABLED", "")
	t.Setenv("ROUTER_DEPLOYMENT_MODE", "selfhosted")
	t.Setenv("ROUTER_INTERNAL_SERVICE_TOKEN", "linked-token")
	require.False(t, uiEnabledFromEnv())
	t.Setenv("ROUTER_UI_ENABLED", "true")
	require.True(t, uiEnabledFromEnv())
	t.Setenv("ROUTER_UI_ENABLED", "")
	t.Setenv("ROUTER_DEPLOYMENT_MODE", "managed")
	require.True(t, uiEnabledFromEnv())
}

type directoryExportSource struct {
	*policyregistry.DirectoryServingStore
	dir string
}

func (s directoryExportSource) ReadPolicyPayload(_ context.Context, ref policyregistry.ObjectRef) ([]byte, error) {
	return policyregistry.ReadDirectoryPolicyPayload(s.dir, s.RootURI(), ref)
}
