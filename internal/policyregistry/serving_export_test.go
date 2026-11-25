package policyregistry_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/rosterdata"
	"weave-os/router/smoke/hmmstub"
)

const (
	exportSourceGeneration = 1795564814288177
	exportClassifierURL    = "http://classifier:8080"
)

var errReadOnlySource = errors.New("export source is read-only")

type gcsShapedExportSource struct {
	root    string
	objects map[string][]byte
	writes  int
}

func newGCSShapedExportSource(t *testing.T) *gcsShapedExportSource {
	t.Helper()
	graph, err := hmmstub.NewServingGraph("https://hmm-stub.invalid")
	require.NoError(t, err)
	return &gcsShapedExportSource{root: hmmstub.RegistryRoot, objects: graph.Objects()}
}

func (s *gcsShapedExportSource) RootURI() string { return s.root }

func (s *gcsShapedExportSource) relative(ref policyregistry.ObjectRef) (string, error) {
	if ref.Generation <= 0 {
		return "", errors.New("source reads require a storage generation")
	}
	relative, found := strings.CutPrefix(ref.URI, s.root+"/")
	if !found {
		return "", policyregistry.ErrNotFound
	}
	return relative, nil
}

func (s *gcsShapedExportSource) ReadServingObject(_ context.Context, kind policyregistry.ServingKind, ref policyregistry.ObjectRef) (policyregistry.ServingManifest, []byte, error) {
	relative, err := s.relative(ref)
	if err != nil {
		return nil, nil, err
	}
	payload, exists := s.objects[relative]
	if !exists {
		return nil, nil, policyregistry.ErrNotFound
	}
	manifest, err := policyregistry.DecodeServingObject(payload, s.root, kind, ref)
	if err != nil {
		return nil, nil, err
	}
	return manifest, payload, nil
}

func (s *gcsShapedExportSource) ReadPolicyPayload(_ context.Context, ref policyregistry.ObjectRef) ([]byte, error) {
	relative, err := s.relative(ref)
	if err != nil {
		return nil, err
	}
	payload, exists := s.objects[relative]
	if !exists {
		return nil, policyregistry.ErrNotFound
	}
	return payload, nil
}

func (s *gcsShapedExportSource) ReadServingPolicy(ctx context.Context, ref policyregistry.ObjectRef) (*rosterdata.Roster, error) {
	payload, err := s.ReadPolicyPayload(ctx, ref)
	if err != nil {
		return nil, err
	}
	return rosterdata.Parse(payload)
}

func (s *gcsShapedExportSource) ReadServingState(_ context.Context, target policyregistry.ServingTarget) (policyregistry.ServingStateSnapshot, error) {
	statePath, err := policyregistry.DirectoryServingStatePath(target)
	if err != nil {
		return policyregistry.ServingStateSnapshot{}, err
	}
	var state policyregistry.ServingControlState
	if err := policyregistry.StrictDecode(s.objects[statePath], &state); err != nil {
		return policyregistry.ServingStateSnapshot{}, err
	}
	return policyregistry.ServingStateSnapshot{State: state, Generation: exportSourceGeneration}, nil
}

func (s *gcsShapedExportSource) CompareAndSwapServingState(context.Context, policyregistry.ServingControlState, int64) (policyregistry.ServingStateSnapshot, error) {
	s.writes++
	return policyregistry.ServingStateSnapshot{}, errReadOnlySource
}

func (s *gcsShapedExportSource) ReadClassifierRuntimeBinding(context.Context, policyregistry.Environment, string) (policyregistry.ResolvedClassifierRuntimeBinding, error) {
	return policyregistry.ResolvedClassifierRuntimeBinding{}, errors.New("export must not depend on the managed classifier runtime binding")
}

func selfHostedExportRequest(dir string) policyregistry.SelfHostedExportRequest {
	return policyregistry.SelfHostedExportRequest{
		Target: hmmstub.ServingTarget,
		Worker: policyregistry.WorkerIdentity{
			Target: hmmstub.ServingTarget, Project: policyregistry.SelfHostedServingProject, Region: policyregistry.SelfHostedServingRegion,
			Revision: policyregistry.SelfHostedServingRevision, ImageDigest: hmmstub.ServingImageDigest,
		},
		ClassifierURL: exportClassifierURL,
		Dir:           dir,
	}
}

func exportedWorker(exported policyregistry.SelfHostedExport) (policyregistry.WorkerIdentity, policyregistry.ObjectRef) {
	env := exported.RouterEnv
	worker := policyregistry.WorkerIdentity{
		Target: env.Target, Project: env.Project, Region: env.Region, Revision: env.Revision, ImageDigest: env.ImageDigest,
		Configuration: policyregistry.ObjectRef{URI: env.ConfigurationURI, SHA256: env.ConfigurationSHA256, Generation: 1},
	}
	return worker, policyregistry.ObjectRef{URI: env.SelectionSetURI, SHA256: env.SelectionSetSHA256, Generation: 1}
}

type unroutedRouter struct{}

func (unroutedRouter) Route(context.Context, router.Request) (router.Decision, error) {
	return router.Decision{}, errors.New("export tests never route")
}

func noRouters(context.Context, policyregistry.Candidate) (map[router.Strategy]router.Router, error) {
	return map[router.Strategy]router.Router{}, nil
}

func TestExportSelfHostedServingGraphBootsThroughDirectoryStore(t *testing.T) {
	source := newGCSShapedExportSource(t)
	before := make(map[string]string, len(source.objects))
	for relative, payload := range source.objects {
		before[relative] = policyregistry.Digest(payload)
	}
	dir := t.TempDir()

	exported, err := policyregistry.ExportSelfHostedServingGraph(context.Background(), source, selfHostedExportRequest(dir))
	require.NoError(t, err)

	require.Zero(t, source.writes)
	after := make(map[string]string, len(source.objects))
	for relative, payload := range source.objects {
		after[relative] = policyregistry.Digest(payload)
	}
	require.Equal(t, before, after)

	require.Equal(t, hmmstub.ServingTarget, exported.Source.Target)
	require.Equal(t, hmmstub.ServingSelectionSetSHA256, exported.Source.SelectionSetSHA256)
	require.Equal(t, policyregistry.SelfHostedServingProject, exported.RouterEnv.Project)
	require.Equal(t, hmmstub.ServingImageDigest, exported.RouterEnv.ImageDigest)
	require.Equal(t, "classifier/release.json", exported.Classifier.ReleaseManifestPath)
	require.Equal(t, "classifier/selection.json", exported.Classifier.SelectionPath)
	require.Equal(t, exported.Classifier.ReleaseManifestPath, exported.ClassifierEnv.ReleaseManifestPath)
	require.Equal(t, exported.Classifier.SelectionPath, exported.ClassifierEnv.SelectionPath)
	require.Equal(t, "sha256:"+strings.Repeat("b", 64), exported.Classifier.ImageDigest)

	worker, selectionRef := exportedWorker(exported)
	store, err := policyregistry.NewDirectoryServingStore(dir)
	require.NoError(t, err)
	cache, err := policyregistry.NewServingRuntimeCache(store, noRouters)
	require.NoError(t, err)
	_, err = cache.PrepareWorker(context.Background(), worker, selectionRef)
	require.NoError(t, err)

	releaseBytes, err := os.ReadFile(filepath.Join(dir, exported.ClassifierEnv.ReleaseManifestPath))
	require.NoError(t, err)
	binding, err := store.ReadClassifierRuntimeBinding(context.Background(), policyregistry.EnvironmentProd, policyregistry.Digest(releaseBytes))
	require.NoError(t, err)
	require.Equal(t, exportClassifierURL, binding.Binding.WorkerURL)
}

func TestExportSelfHostedClassifierSelectionIDMatchesSidecarFiles(t *testing.T) {
	dir := t.TempDir()
	exported, err := policyregistry.ExportSelfHostedServingGraph(context.Background(), newGCSShapedExportSource(t), selfHostedExportRequest(dir))
	require.NoError(t, err)

	selectionBytes, err := os.ReadFile(filepath.Join(dir, exported.ClassifierEnv.SelectionPath))
	require.NoError(t, err)
	releaseBytes, err := os.ReadFile(filepath.Join(dir, exported.ClassifierEnv.ReleaseManifestPath))
	require.NoError(t, err)
	require.Equal(t, exported.RouterEnv.SelectionSetSHA256, policyregistry.Digest(selectionBytes))

	var selection policyregistry.SelectionSetV4
	require.NoError(t, policyregistry.StrictDecode(selectionBytes, &selection))
	require.Equal(t, policyregistry.Digest(releaseBytes), selection.Classifier.Release.SHA256)

	store, err := policyregistry.NewDirectoryServingStore(dir)
	require.NoError(t, err)
	_, selectionRef := exportedWorker(exported)
	var classifierSelectionID string
	cache, err := policyregistry.NewServingRuntimeCache(store, func(_ context.Context, candidate policyregistry.Candidate) (map[router.Strategy]router.Router, error) {
		classifierSelectionID = candidate.ClassifierSelectionID
		return map[router.Strategy]router.Router{router.PrimaryRoutingStrategy: unroutedRouter{}}, nil
	})
	require.NoError(t, err)
	_, err = cache.Snapshot(context.Background(), policyregistry.SessionReleaseBinding{
		Target: hmmstub.ServingTarget, Selection: policyregistry.ServingSelection{Release: selection.Code.Candidate, Binding: selectionRef},
	})
	require.NoError(t, err)
	require.Equal(t, policyregistry.Digest(selectionBytes), classifierSelectionID)
}

func TestExportSelfHostedServingGraphRejectsTamperedSourceObjects(t *testing.T) {
	for _, kind := range []string{"candidate", "release", "policy"} {
		t.Run(kind, func(t *testing.T) {
			source := newGCSShapedExportSource(t)
			tampered := tamperedObjectPath(t, source, kind)
			source.objects[tampered] = append(source.objects[tampered], ' ')
			_, err := policyregistry.ExportSelfHostedServingGraph(context.Background(), source, selfHostedExportRequest(t.TempDir()))
			require.Error(t, err)
		})
	}
}

func TestExportSelfHostedServingGraphReadBackRejectsTamperedCopies(t *testing.T) {
	dir := t.TempDir()
	exported, err := policyregistry.ExportSelfHostedServingGraph(context.Background(), newGCSShapedExportSource(t), selfHostedExportRequest(dir))
	require.NoError(t, err)
	worker, selectionRef := exportedWorker(exported)
	paths, err := filepath.Glob(filepath.Join(dir, "artifacts", "*.json"))
	require.NoError(t, err)
	policies, err := filepath.Glob(filepath.Join(dir, "router_policy", "v1", "policies", "sha256", "*.json"))
	require.NoError(t, err)
	paths = append(paths, policies...)
	require.NotEmpty(t, paths)
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			original, err := os.ReadFile(path)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(path, append(append([]byte{}, original...), ' '), 0o600))
			t.Cleanup(func() { require.NoError(t, os.WriteFile(path, original, 0o600)) })
			require.Error(t, policyregistry.VerifySelfHostedServingGraph(context.Background(), dir, worker, selectionRef))
		})
	}
	require.NoError(t, policyregistry.VerifySelfHostedServingGraph(context.Background(), dir, worker, selectionRef))
}

func TestExportSelfHostedServingGraphRequiresTheCandidateRouterImage(t *testing.T) {
	request := selfHostedExportRequest(t.TempDir())
	request.Worker.ImageDigest = "sha256:" + strings.Repeat("9", 64)
	_, err := policyregistry.ExportSelfHostedServingGraph(context.Background(), newGCSShapedExportSource(t), request)
	require.ErrorContains(t, err, "self-hosted workers must run the exact candidate image")
}

func TestExportSelfHostedServingGraphRefusesNonEmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "stale"), []byte("x"), 0o600))
	_, err := policyregistry.ExportSelfHostedServingGraph(context.Background(), newGCSShapedExportSource(t), selfHostedExportRequest(dir))
	require.ErrorContains(t, err, "is not empty")
}

func tamperedObjectPath(t *testing.T, source *gcsShapedExportSource, kind string) string {
	t.Helper()
	statePath, err := policyregistry.DirectoryServingStatePath(hmmstub.ServingTarget)
	require.NoError(t, err)
	var state policyregistry.ServingControlState
	require.NoError(t, json.Unmarshal(source.objects[statePath], &state))
	selectionRelative := strings.TrimPrefix(state.Activations[state.CurrentActivationID].SelectionSet.URI, source.root+"/")
	var selection policyregistry.SelectionSetV4
	require.NoError(t, policyregistry.StrictDecode(source.objects[selectionRelative], &selection))
	uri := map[string]string{
		"candidate": selection.Code.Candidate.URI,
		"release":   selection.Classifier.Release.URI,
		"policy":    selection.DefaultPolicy.URI,
	}[kind]
	return strings.TrimPrefix(uri, source.root+"/")
}

func TestReadSelfHostedServingSourceReportsTheExportedActivation(t *testing.T) {
	dir := t.TempDir()
	exported, err := policyregistry.ExportSelfHostedServingGraph(context.Background(), newGCSShapedExportSource(t), selfHostedExportRequest(dir))
	require.NoError(t, err)
	worker, _ := exportedWorker(exported)

	source, err := policyregistry.ReadSelfHostedServingSource(dir, worker.Configuration)
	require.NoError(t, err)
	require.Equal(t, exported.Source, source)
}

func TestExportSelfHostedKeepsTheReleaseWhenTheCustomerImageMatches(t *testing.T) {
	source := newGCSShapedExportSource(t)
	dir := t.TempDir()
	request := selfHostedExportRequest(dir)
	request.ClassifierImageDigest = "sha256:" + strings.Repeat("b", 64)
	exported, err := policyregistry.ExportSelfHostedServingGraph(context.Background(), source, request)
	require.NoError(t, err)
	require.False(t, exported.Classifier.Rebound)
	require.Empty(t, exported.Classifier.RebindReason)
	release, err := os.ReadFile(filepath.Join(dir, exported.ClassifierEnv.ReleaseManifestPath))
	require.NoError(t, err)
	require.Equal(t, sourceClassifierReleaseSHA256(t, source), policyregistry.Digest(release))
}

func TestExportSelfHostedRebindsTheReleaseWhenTheCustomerImageDiffers(t *testing.T) {
	source := newGCSShapedExportSource(t)
	dir := t.TempDir()
	customer := "sha256:" + strings.Repeat("e", 64)
	request := selfHostedExportRequest(dir)
	request.ClassifierImageDigest = customer
	exported, err := policyregistry.ExportSelfHostedServingGraph(context.Background(), source, request)
	require.NoError(t, err)
	require.True(t, exported.Classifier.Rebound)
	require.Contains(t, exported.Classifier.RebindReason, customer)
	require.Equal(t, customer, exported.Classifier.ImageDigest)
	release, err := os.ReadFile(filepath.Join(dir, exported.ClassifierEnv.ReleaseManifestPath))
	require.NoError(t, err)
	var decoded policyregistry.ClassifierReleaseManifestV1
	require.NoError(t, json.Unmarshal(release, &decoded))
	require.Equal(t, customer, decoded.RuntimeImageDigest)
	selectionBytes, err := os.ReadFile(filepath.Join(dir, exported.ClassifierEnv.SelectionPath))
	require.NoError(t, err)
	var selection policyregistry.SelectionSetV4
	require.NoError(t, policyregistry.StrictDecode(selectionBytes, &selection))
	require.Equal(t, policyregistry.Digest(release), selection.Classifier.Release.SHA256)
}

func sourceClassifierReleaseSHA256(t *testing.T, source *gcsShapedExportSource) string {
	t.Helper()
	statePath, err := policyregistry.DirectoryServingStatePath(hmmstub.ServingTarget)
	require.NoError(t, err)
	var state policyregistry.ServingControlState
	require.NoError(t, policyregistry.StrictDecode(source.objects[statePath], &state))
	selectionRelative := strings.TrimPrefix(state.Activations[state.CurrentActivationID].SelectionSet.URI, source.root+"/")
	var selection policyregistry.SelectionSetV4
	require.NoError(t, policyregistry.StrictDecode(source.objects[selectionRelative], &selection))
	return selection.Classifier.Release.SHA256
}

func TestExportSelfHostedUpgradeRebindsSessionsFromThePreviousExport(t *testing.T) {
	ctx := context.Background()
	firstDir := t.TempDir()
	first, err := policyregistry.ExportSelfHostedServingGraph(ctx, newGCSShapedExportSource(t), selfHostedExportRequest(firstDir))
	require.NoError(t, err)
	firstStore, err := policyregistry.NewDirectoryServingStore(firstDir)
	require.NoError(t, err)
	projection := policyregistry.AdmissionProjection{Target: hmmstub.ServingTarget}
	clock := func(context.Context) (time.Time, error) { return time.Now().UTC(), nil }
	session, err := policyregistry.ServingAdmission{Store: firstStore}.Decide(ctx, policyregistry.SerializedAdmission{Projection: projection, Clock: clock})
	require.NoError(t, err)

	upgradeDir := t.TempDir()
	request := selfHostedExportRequest(upgradeDir)
	request.PreviousDir = firstDir
	request.ClassifierImageDigest = "sha256:" + strings.Repeat("e", 64)
	upgrade, err := policyregistry.ExportSelfHostedServingGraph(ctx, newGCSShapedExportSource(t), request)
	require.NoError(t, err)
	require.NotEqual(t, first.RouterEnv.SelectionSetSHA256, upgrade.RouterEnv.SelectionSetSHA256)

	upgradeStore, err := policyregistry.NewDirectoryServingStore(upgradeDir)
	require.NoError(t, err)
	snapshot, err := upgradeStore.ReadServingState(ctx, hmmstub.ServingTarget)
	require.NoError(t, err)
	require.Len(t, snapshot.State.Activations, 2)
	require.Equal(t, int64(2), snapshot.State.Sequence)
	retired := snapshot.State.Activations[session.ActivationID]
	require.NotNil(t, retired.WithdrawnAt)
	require.Equal(t, snapshot.State.CurrentActivationID, retired.ReplacementID)

	rebound, err := policyregistry.ServingAdmission{Store: upgradeStore}.Decide(ctx, policyregistry.SerializedAdmission{Projection: projection, Previous: &session, Clock: clock})
	require.NoError(t, err)
	require.Equal(t, snapshot.State.CurrentActivationID, rebound.ActivationID)
	require.Equal(t, int64(2), rebound.BindingGeneration)

	replayDir := t.TempDir()
	request.Dir = replayDir
	request.PreviousDir = upgradeDir
	_, err = policyregistry.ExportSelfHostedServingGraph(ctx, newGCSShapedExportSource(t), request)
	require.NoError(t, err)
	replayStore, err := policyregistry.NewDirectoryServingStore(replayDir)
	require.NoError(t, err)
	replayed, err := replayStore.ReadServingState(ctx, hmmstub.ServingTarget)
	require.NoError(t, err)
	require.Equal(t, snapshot.State, replayed.State)
}

func TestExportSelfHostedUpgradeRejectsATamperedPreviousExport(t *testing.T) {
	ctx := context.Background()
	firstDir := t.TempDir()
	_, err := policyregistry.ExportSelfHostedServingGraph(ctx, newGCSShapedExportSource(t), selfHostedExportRequest(firstDir))
	require.NoError(t, err)
	statePath, err := policyregistry.DirectoryServingStatePath(hmmstub.ServingTarget)
	require.NoError(t, err)
	var state policyregistry.ServingControlState
	statePayload, err := os.ReadFile(filepath.Join(firstDir, filepath.FromSlash(statePath)))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(statePayload, &state))
	root, err := policyregistry.NewDirectoryServingStore(firstDir)
	require.NoError(t, err)
	selectionPath := filepath.Join(firstDir, filepath.FromSlash(strings.TrimPrefix(state.Activations[state.CurrentActivationID].SelectionSet.URI, root.RootURI()+"/")))
	payload, err := os.ReadFile(selectionPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(selectionPath, append(payload, ' '), 0o600))

	request := selfHostedExportRequest(t.TempDir())
	request.PreviousDir = firstDir
	_, err = policyregistry.ExportSelfHostedServingGraph(ctx, newGCSShapedExportSource(t), request)
	require.ErrorContains(t, err, "does not match its digest")
}
