package policyregistry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/google/uuid"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
)

const (
	SelfHostedServingProject  = "selfhosted"
	SelfHostedServingRegion   = "local"
	SelfHostedServingRevision = "selfhosted-router"

	SelfHostedClassifierReleasePath   = "classifier/release.json"
	SelfHostedClassifierSelectionPath = "classifier/selection.json"

	SelfHostedServingRoot = "gs://weave-router-selfhosted/serving"

	selfHostedActor              = "policyctl-export-selfhosted"
	selfHostedRouterOrigin       = "https://router.selfhosted.invalid"
	selfHostedClassifierRevision = "selfhosted-classifier"
	selfHostedProvenanceSchemaV1 = "router_selfhosted_export_provenance_v1"
)

var selfHostedActivationNamespace = uuid.MustParse("6f1d3c2a-8b4e-4f5a-9c7d-2e1b0a9f8c3d")

type selfHostedProvenance struct {
	SchemaVersion string        `json:"schema_version"`
	Target        ServingTarget `json:"target"`
	ActivationID  string        `json:"activation_id"`
	SelectionSet  ObjectRef     `json:"selection_set"`
	Configuration ObjectRef     `json:"configuration"`
	Attestation   ObjectRef     `json:"attestation"`
}

type SelfHostedExportStore interface {
	ServingStore
	ReadPolicyPayload(context.Context, ObjectRef) ([]byte, error)
}

type SelfHostedExportRequest struct {
	Target                ServingTarget
	ActivationID          string
	Worker                WorkerIdentity
	ClassifierURL         string
	ClassifierImageDigest string
	Dir                   string
	PreviousDir           string
}

type SelfHostedRouterEnv struct {
	Target                  ServingTarget `json:"ROUTER_SERVING_TARGET"`
	Project                 string        `json:"ROUTER_SERVING_PROJECT"`
	Region                  string        `json:"ROUTER_SERVING_REGION"`
	Revision                string        `json:"ROUTER_SERVING_REVISION"`
	ImageDigest             string        `json:"ROUTER_SERVING_IMAGE_DIGEST"`
	ConfigurationURI        string        `json:"ROUTER_SERVING_CONFIGURATION_URI"`
	ConfigurationSHA256     string        `json:"ROUTER_SERVING_CONFIGURATION_SHA256"`
	ConfigurationGeneration string        `json:"ROUTER_SERVING_CONFIGURATION_GENERATION"`
	SelectionSetURI         string        `json:"ROUTER_SERVING_SELECTION_SET_URI"`
	SelectionSetSHA256      string        `json:"ROUTER_SERVING_SELECTION_SET_SHA256"`
	SelectionSetGeneration  string        `json:"ROUTER_SERVING_SELECTION_SET_GENERATION"`
}

type SelfHostedClassifierExport struct {
	ImageDigest         string `json:"image_digest"`
	PackageURI          string `json:"package_uri"`
	PackageSHA256       string `json:"package_sha256"`
	ReleaseManifestPath string `json:"release_manifest_path"`
	SelectionPath       string `json:"selection_path"`
	Rebound             bool   `json:"rebound"`
	RebindReason        string `json:"rebind_reason,omitempty"`
}

type SelfHostedClassifierEnv struct {
	ReleaseManifestPath string `json:"CLASSIFIER_LOCAL_RELEASE_MANIFEST_PATH"`
	SelectionPath       string `json:"CLASSIFIER_LOCAL_SELECTION_PATH"`
}

type SelfHostedExportSource struct {
	Target             ServingTarget `json:"target"`
	ActivationID       string        `json:"activation_id"`
	SelectionSetSHA256 string        `json:"selection_set_sha256"`
}

type SelfHostedExport struct {
	RouterEnv     SelfHostedRouterEnv        `json:"router_env"`
	Classifier    SelfHostedClassifierExport `json:"classifier"`
	ClassifierEnv SelfHostedClassifierEnv    `json:"classifier_env"`
	Source        SelfHostedExportSource     `json:"source"`
}

type selfHostedGraph struct {
	root    string
	objects map[string][]byte
}

func (g selfHostedGraph) copyExact(namespace string, source ObjectRef, payload []byte) (ObjectRef, error) {
	if Digest(payload) != source.SHA256 {
		return ObjectRef{}, fmt.Errorf("source object %s does not match its pinned digest", source.URI)
	}
	return g.put(namespace, payload), nil
}

func (g selfHostedGraph) put(namespace string, payload []byte) ObjectRef {
	digest := Digest(payload)
	relative := namespace + digest + ".json"
	g.objects[relative] = payload
	return ObjectRef{URI: g.root + "/" + relative, SHA256: digest, Generation: directoryServingGeneration}
}

func ExportSelfHostedServingGraph(ctx context.Context, source SelfHostedExportStore, request SelfHostedExportRequest) (SelfHostedExport, error) {
	environment, err := request.Target.Environment()
	if err != nil {
		return SelfHostedExport{}, err
	}
	if err := validateSelfHostedRequest(request); err != nil {
		return SelfHostedExport{}, err
	}
	classifierURL, err := selfHostedClassifierOrigin(request.ClassifierURL)
	if err != nil {
		return SelfHostedExport{}, err
	}
	if err := ensureEmptyExportDir(request.Dir); err != nil {
		return SelfHostedExport{}, err
	}
	activation, err := readSelfHostedActivation(ctx, source, request)
	if err != nil {
		return SelfHostedExport{}, err
	}
	pinned, err := readSelfHostedPinned(ctx, source, activation, request.Worker.ImageDigest)
	if err != nil {
		return SelfHostedExport{}, err
	}
	graph := selfHostedGraph{root: SelfHostedServingRoot, objects: make(map[string][]byte)}
	previous, err := carrySelfHostedHistory(ctx, request, graph)
	if err != nil {
		return SelfHostedExport{}, err
	}
	copied, err := copySelfHostedObjects(ctx, source, &graph, pinned, request.ClassifierImageDigest)
	if err != nil {
		return SelfHostedExport{}, err
	}
	bound, err := rebindSelfHostedGraph(&graph, request, pinned, copied, previous, classifierURL, environment)
	if err != nil {
		return SelfHostedExport{}, err
	}
	if err := WriteDirectoryServingRegistry(request.Dir, graph.root, graph.objects); err != nil {
		return SelfHostedExport{}, err
	}
	worker := request.Worker
	worker.Configuration = bound.configurationRef
	if err := VerifySelfHostedServingGraph(ctx, request.Dir, worker, bound.selectionRef); err != nil {
		return SelfHostedExport{}, fmt.Errorf("exported graph failed read-back: %w", err)
	}
	return selfHostedExportDocument(request, activation, pinned, copied, bound), nil
}

type selfHostedActivationSource struct {
	pinned    Activation
	selection *SelectionSetV4
}

type selfHostedPinnedObjects struct {
	activation       Activation
	selection        *SelectionSetV4
	release          *ClassifierReleaseManifestV1
	candidatePayload []byte
	releasePayload   []byte
}

type selfHostedCopied struct {
	releasePayload []byte
	rebound        bool
	imageDigest    string
	candidate      ObjectRef
	release        ObjectRef
	defaultPolicy  PolicyObject
	profiles       map[string]PolicyObject
	strategies     map[router.Strategy]PolicyObject
}

type selfHostedBoundRefs struct {
	selectionRef     ObjectRef
	configurationRef ObjectRef
}

func validateSelfHostedRequest(request SelfHostedExportRequest) error {
	if request.Worker.Target != request.Target || request.Worker.Project == "" || request.Worker.Region == "" || request.Worker.Revision == "" || !validImageDigest(request.Worker.ImageDigest) {
		return errors.New("self-hosted export requires a worker identity for the exported target with an exact router image digest")
	}
	return nil
}

func readSelfHostedActivation(ctx context.Context, source SelfHostedExportStore, request SelfHostedExportRequest) (selfHostedActivationSource, error) {
	snapshot, err := source.ReadServingState(ctx, request.Target)
	if err != nil {
		return selfHostedActivationSource{}, fmt.Errorf("read serving state for %s: %w", request.Target, err)
	}
	activationID := request.ActivationID
	if activationID == "" {
		activationID = snapshot.State.CurrentActivationID
	}
	activation, exists := snapshot.State.Activations[activationID]
	if !exists || activation.WithdrawnAt != nil {
		return selfHostedActivationSource{}, fmt.Errorf("serving target %s has no servable activation %q", request.Target, activationID)
	}
	selectionManifest, _, err := readServingFamily(ctx, source, ServingSelectionSet, activation.SelectionSet)
	if err != nil {
		return selfHostedActivationSource{}, fmt.Errorf("read selection set %s: %w", activation.SelectionSet.SHA256, err)
	}
	selection, isV4 := selectionManifest.(*SelectionSetV4)
	if !isV4 {
		return selfHostedActivationSource{}, fmt.Errorf("activation %s selects %T; self-hosted export supports only v4 classifier selection sets", activation.ID, selectionManifest)
	}
	return selfHostedActivationSource{pinned: activation, selection: selection}, nil
}

func readSelfHostedPinned(ctx context.Context, source SelfHostedExportStore, activation selfHostedActivationSource, routerImageDigest string) (selfHostedPinnedObjects, error) {
	selection := activation.selection
	candidateManifest, candidatePayload, err := readServingFamily(ctx, source, ServingCandidate, selection.Code.Candidate)
	if err != nil {
		return selfHostedPinnedObjects{}, fmt.Errorf("read candidate %s: %w", selection.Code.Candidate.SHA256, err)
	}
	candidate, isV3 := candidateManifest.(*CandidateV3)
	if !isV3 {
		return selfHostedPinnedObjects{}, errors.New("v4 selection set does not pin a v3 router candidate")
	}
	if candidate.RouterImageDigest != routerImageDigest {
		return selfHostedPinnedObjects{}, fmt.Errorf("router image digest %s differs from the exported candidate's %s; self-hosted workers must run the exact candidate image", routerImageDigest, candidate.RouterImageDigest)
	}
	releaseManifest, releasePayload, err := readServingFamily(ctx, source, ServingClassifierReleases, selection.Classifier.Release)
	if err != nil {
		return selfHostedPinnedObjects{}, fmt.Errorf("read classifier release %s: %w", selection.Classifier.Release.SHA256, err)
	}
	release, isRelease := releaseManifest.(*ClassifierReleaseManifestV1)
	if !isRelease {
		return selfHostedPinnedObjects{}, errors.New("registry returned the wrong classifier release manifest kind")
	}
	if release.Variants[policy.ClassifierVariantPrimary].PackageWeaveDataRef == nil {
		return selfHostedPinnedObjects{}, errors.New("classifier release primary variant has no package reference")
	}
	for _, variantID := range selection.Classifier.EnabledVariants {
		if kind := release.Variants[variantID].Kind; kind != ClassifierVariantKindHMMPackage {
			return selfHostedPinnedObjects{}, fmt.Errorf("enabled classifier variant %q is a %q; the self-hosted classifier serves only %q variants", variantID, kind, ClassifierVariantKindHMMPackage)
		}
	}
	return selfHostedPinnedObjects{activation: activation.pinned, selection: selection, release: release, candidatePayload: candidatePayload, releasePayload: releasePayload}, nil
}

func selfHostedReleasePayload(pinned selfHostedPinnedObjects, classifierImageDigest string) ([]byte, bool, error) {
	if classifierImageDigest == "" || classifierImageDigest == pinned.release.RuntimeImageDigest {
		return pinned.releasePayload, false, nil
	}
	release := *pinned.release
	release.RuntimeImageDigest = classifierImageDigest
	payload, err := CanonicalBytes(release)
	if err != nil {
		return nil, false, fmt.Errorf("encode re-bound classifier release: %w", err)
	}
	return payload, true, nil
}

func copySelfHostedObjects(ctx context.Context, source SelfHostedExportStore, graph *selfHostedGraph, pinned selfHostedPinnedObjects, classifierImageDigest string) (selfHostedCopied, error) {
	selection := pinned.selection
	candidateRef, err := graph.copyExact(ServingArtifactsNamespace, selection.Code.Candidate, pinned.candidatePayload)
	if err != nil {
		return selfHostedCopied{}, err
	}
	releasePayload, rebound, err := selfHostedReleasePayload(pinned, classifierImageDigest)
	if err != nil {
		return selfHostedCopied{}, err
	}
	var releaseRef ObjectRef
	if rebound {
		releaseRef = graph.put(ServingArtifactsNamespace, releasePayload)
	} else if releaseRef, err = graph.copyExact(ServingArtifactsNamespace, selection.Classifier.Release, releasePayload); err != nil {
		return selfHostedCopied{}, err
	}
	copyPolicy := func(reference PolicyObject) (PolicyObject, error) {
		return copySelfHostedPolicy(ctx, source, graph, reference)
	}
	defaultPolicy, err := copyPolicy(selection.DefaultPolicy)
	if err != nil {
		return selfHostedCopied{}, err
	}
	profiles := make(map[string]PolicyObject, len(selection.Profiles))
	for key, reference := range selection.Profiles {
		if profiles[key], err = copyPolicy(reference); err != nil {
			return selfHostedCopied{}, err
		}
	}
	strategies := make(map[router.Strategy]PolicyObject, len(selection.StrategyPolicies))
	for strategy, reference := range selection.StrategyPolicies {
		if strategies[strategy], err = copyPolicy(reference); err != nil {
			return selfHostedCopied{}, err
		}
	}
	imageDigest := pinned.release.RuntimeImageDigest
	if rebound {
		imageDigest = classifierImageDigest
	}
	return selfHostedCopied{candidate: candidateRef, release: releaseRef, releasePayload: releasePayload, rebound: rebound, imageDigest: imageDigest, defaultPolicy: defaultPolicy, profiles: profiles, strategies: strategies}, nil
}

func copySelfHostedPolicy(ctx context.Context, source SelfHostedExportStore, graph *selfHostedGraph, reference PolicyObject) (PolicyObject, error) {
	sourceRef := ObjectRef{URI: reference.URI, SHA256: reference.SHA256, Generation: reference.Generation}
	payload, err := source.ReadPolicyPayload(ctx, sourceRef)
	if err != nil {
		return PolicyObject{}, fmt.Errorf("read policy %s: %w", reference.SHA256, err)
	}
	copied, err := graph.copyExact(PolicyObjectPrefix, sourceRef, payload)
	if err != nil {
		return PolicyObject{}, err
	}
	return PolicyObject{URI: copied.URI, SHA256: copied.SHA256, SchemaVersion: reference.SchemaVersion, Generation: copied.Generation}, nil
}

func rebindSelfHostedGraph(graph *selfHostedGraph, request SelfHostedExportRequest, pinned selfHostedPinnedObjects, copied selfHostedCopied, previous *ServingControlState, classifierURL string, environment Environment) (selfHostedBoundRefs, error) {
	worker := request.Worker
	provenance, err := CanonicalBytes(selfHostedProvenance{
		SchemaVersion: selfHostedProvenanceSchemaV1,
		Target:        request.Target, ActivationID: pinned.activation.ID, SelectionSet: pinned.activation.SelectionSet,
		Configuration: pinned.selection.Code.Router.Configuration, Attestation: pinned.selection.Code.Attestation,
	})
	if err != nil {
		return selfHostedBoundRefs{}, err
	}
	configurationRef := graph.put(ServingArtifactsNamespace, provenance)
	worker.Configuration = configurationRef

	selection := *pinned.selection
	selection.Code = CandidateCodeV4{
		Candidate: copied.candidate, Project: worker.Project, Region: worker.Region,
		Router: RevisionBinding{
			Name: worker.Revision, URL: selfHostedRouterOrigin, Audience: selfHostedRouterOrigin,
			ImageDigest: worker.ImageDigest, Configuration: configurationRef,
		},
		Attestation: configurationRef,
	}
	selection.Classifier = ClassifierServiceBinding{Release: copied.release, EnabledVariants: pinned.selection.Classifier.EnabledVariants}
	selection.DefaultPolicy = copied.defaultPolicy
	selection.Profiles = copied.profiles
	selection.StrategyPolicies = copied.strategies
	selectionPayload, err := CanonicalBytes(selection)
	if err != nil {
		return selfHostedBoundRefs{}, fmt.Errorf("encode self-hosted selection set: %w", err)
	}
	selectionRef := graph.put(ServingArtifactsNamespace, selectionPayload)

	state, err := json.Marshal(selfHostedServingState(previous, request.Target, Activation{
		SelectionSet: selectionRef, ActivatedAt: pinned.activation.ActivatedAt,
		Proposal: configurationRef, RequestID: pinned.activation.ID, Actor: selfHostedActor, WorkflowActor: selfHostedActor,
	}))
	if err != nil {
		return selfHostedBoundRefs{}, fmt.Errorf("encode self-hosted serving state: %w", err)
	}
	statePath, err := DirectoryServingStatePath(request.Target)
	if err != nil {
		return selfHostedBoundRefs{}, err
	}
	graph.objects[statePath] = state
	binding, err := json.Marshal(ClassifierRuntimeBinding{
		SchemaVersion: ClassifierRuntimeBindingSchemaV1, Environment: environment, ProjectID: worker.Project,
		ReleaseSHA256: copied.release.SHA256, WorkerRevision: selfHostedClassifierRevision, WorkerURL: classifierURL, WorkerAudience: classifierURL,
		ImageDigest: copied.imageDigest, ConfigurationSHA256: configurationRef.SHA256,
	})
	if err != nil {
		return selfHostedBoundRefs{}, fmt.Errorf("encode self-hosted classifier runtime binding: %w", err)
	}
	graph.objects[DirectoryServingRuntimeBindingPath(environment, copied.release.SHA256)] = binding
	graph.objects[SelfHostedClassifierReleasePath] = copied.releasePayload
	graph.objects[SelfHostedClassifierSelectionPath] = selectionPayload
	return selfHostedBoundRefs{selectionRef: selectionRef, configurationRef: configurationRef}, nil
}

func rebindReason(copied selfHostedCopied, pinned selfHostedPinnedObjects) string {
	if !copied.rebound {
		return ""
	}
	return "classifier image " + copied.imageDigest + " differs from the released image " + pinned.release.RuntimeImageDigest + "; release re-bound to the customer image"
}

func selfHostedExportDocument(request SelfHostedExportRequest, activation selfHostedActivationSource, pinned selfHostedPinnedObjects, copied selfHostedCopied, bound selfHostedBoundRefs) SelfHostedExport {
	worker := request.Worker
	primary := pinned.release.Variants[policy.ClassifierVariantPrimary]
	return SelfHostedExport{
		RouterEnv: SelfHostedRouterEnv{
			Target: request.Target, Project: worker.Project, Region: worker.Region, Revision: worker.Revision, ImageDigest: worker.ImageDigest,
			ConfigurationURI: bound.configurationRef.URI, ConfigurationSHA256: bound.configurationRef.SHA256, ConfigurationGeneration: fmt.Sprint(bound.configurationRef.Generation),
			SelectionSetURI: bound.selectionRef.URI, SelectionSetSHA256: bound.selectionRef.SHA256, SelectionSetGeneration: fmt.Sprint(bound.selectionRef.Generation),
		},
		Classifier: SelfHostedClassifierExport{
			ImageDigest: copied.imageDigest, PackageURI: primary.PackageWeaveDataRef.StorageURI, PackageSHA256: primary.ArtifactSHA256,
			Rebound: copied.rebound, RebindReason: rebindReason(copied, pinned),
			ReleaseManifestPath: SelfHostedClassifierReleasePath, SelectionPath: SelfHostedClassifierSelectionPath,
		},
		ClassifierEnv: SelfHostedClassifierEnv{ReleaseManifestPath: SelfHostedClassifierReleasePath, SelectionPath: SelfHostedClassifierSelectionPath},
		Source:        SelfHostedExportSource{Target: request.Target, ActivationID: activation.pinned.ID, SelectionSetSHA256: activation.pinned.SelectionSet.SHA256},
	}
}

func VerifySelfHostedServingGraph(ctx context.Context, dir string, worker WorkerIdentity, selectionRef ObjectRef) error {
	store, err := NewDirectoryServingStore(dir)
	if err != nil {
		return err
	}
	cache, err := NewServingRuntimeCache(store, func(context.Context, Candidate) (map[router.Strategy]router.Router, error) {
		return map[router.Strategy]router.Router{}, nil
	})
	if err != nil {
		return err
	}
	if _, err := cache.PrepareWorker(ctx, worker, selectionRef); err != nil {
		return err
	}
	if _, err := readDirectoryObject(dir, store.RootURI(), worker.Configuration, ServingArtifactsNamespace); err != nil {
		return fmt.Errorf("worker configuration: %w", err)
	}
	return nil
}

func selfHostedClassifierOrigin(raw string) (string, error) {
	origin := strings.TrimRight(strings.TrimSpace(raw), "/")
	parsed, err := url.Parse(origin)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("classifier URL %q must be an http(s) origin", raw)
	}
	return origin, nil
}

func ensureEmptyExportDir(dir string) error {
	if strings.TrimSpace(dir) == "" {
		return errors.New("self-hosted export requires an output directory")
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect export directory: %w", err)
	}
	if len(entries) != 0 {
		return fmt.Errorf("export directory %s is not empty", dir)
	}
	return nil
}

func ReadSelfHostedServingSource(dir string, configuration ObjectRef) (SelfHostedExportSource, error) {
	store, err := NewDirectoryServingStore(dir)
	if err != nil {
		return SelfHostedExportSource{}, err
	}
	payload, err := readDirectoryObject(dir, store.RootURI(), configuration, ServingArtifactsNamespace)
	if err != nil {
		return SelfHostedExportSource{}, err
	}
	var provenance selfHostedProvenance
	if err := StrictDecode(payload, &provenance); err != nil {
		return SelfHostedExportSource{}, fmt.Errorf("decode self-hosted export provenance: %w", err)
	}
	if provenance.SchemaVersion != selfHostedProvenanceSchemaV1 {
		return SelfHostedExportSource{}, fmt.Errorf("unsupported self-hosted export provenance schema %q", provenance.SchemaVersion)
	}
	return SelfHostedExportSource{Target: provenance.Target, ActivationID: provenance.ActivationID, SelectionSetSHA256: provenance.SelectionSet.SHA256}, nil
}
