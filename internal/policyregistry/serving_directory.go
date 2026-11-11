package policyregistry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"weave-os/router/internal/router/hmm/rosterdata"
)

// LocalServingReleaseSchema tags the manifest of an exported self-hosted V4 release directory.
type LocalServingReleaseSchema string

// LocalServingReleaseSchemaV1 is the only supported local release manifest schema.
const LocalServingReleaseSchemaV1 LocalServingReleaseSchema = "router_local_serving_release_v1"

const (
	localServingReleaseFile      = "serving_release.json"
	selfHostedClassifierRevision = "self-hosted"
)

// LocalServingRelease names the exported V4 selection set a self-hosted Router serves.
type LocalServingRelease struct {
	SchemaVersion LocalServingReleaseSchema `json:"schema_version"`
	RootURI       string                    `json:"root_uri"`
	Target        ServingTarget             `json:"target"`
	SelectionSet  ObjectRef                 `json:"selection_set"`
}

// ServingDirectory serves one exported V4 selection set and the exact immutable objects it
// references from a local copy of the serving registry layout. Only self-hosted composition
// constructs it: the classifier worker is the locally configured origin, never a runtime
// binding read from the registry, and managed bindings keep their Cloud Run origin check.
type ServingDirectory struct {
	dir      string
	release  LocalServingRelease
	selected *SelectionSetV4
	binding  ResolvedClassifierRuntimeBinding
}

// NewServingDirectory loads dir/serving_release.json and binds its classifier release to workerURL.
func NewServingDirectory(dir, workerURL string) (*ServingDirectory, error) {
	if err := validateSelfHostedClassifierWorkerOrigin(workerURL); err != nil {
		return nil, err
	}
	payload, err := readLocalObject(filepath.Join(dir, localServingReleaseFile))
	if err != nil {
		return nil, err
	}
	var release LocalServingRelease
	if err := strictDecode(payload, &release); err != nil {
		return nil, fmt.Errorf("decode local serving release: %w", err)
	}
	return loadServingDirectory(dir, workerURL, release)
}

func loadServingDirectory(dir, workerURL string, release LocalServingRelease) (*ServingDirectory, error) {
	if release.SchemaVersion != LocalServingReleaseSchemaV1 {
		return nil, fmt.Errorf("unsupported local serving release schema %q", release.SchemaVersion)
	}
	if _, err := release.Target.Environment(); err != nil {
		return nil, err
	}
	directory := &ServingDirectory{dir: dir, release: release}
	selection, err := readServing[*SelectionSetV4](context.Background(), directory, ServingSelectionSet, release.SelectionSet)
	if err != nil {
		return nil, fmt.Errorf("local release must pin a V4 selection set: %w", err)
	}
	if selection.Target != release.Target {
		return nil, errors.New("local selection set belongs to another target")
	}
	classifierRelease, err := readServing[*ClassifierReleaseManifestV1](context.Background(), directory, ServingClassifierReleases, selection.Classifier.Release)
	if err != nil {
		return nil, fmt.Errorf("read local classifier release: %w", err)
	}
	directory.selected = selection
	directory.binding, err = selfHostedRuntimeBinding(release.Target, selection.Classifier.Release.SHA256, classifierRelease.RuntimeImageDigest, workerURL)
	if err != nil {
		return nil, err
	}
	return directory, nil
}

func (d *ServingDirectory) Release() LocalServingRelease { return d.release }

// SelectionSet is valid only on a directory returned by NewServingDirectory, which loads it.
func (d *ServingDirectory) SelectionSet() SelectionSetV4 { return *d.selected }

// RootURI is the registry root the exported objects were published under.
func (d *ServingDirectory) RootURI() string { return d.release.RootURI }

// ReadServingObject decodes the exact exported payload of a content-addressed serving object.
func (d *ServingDirectory) ReadServingObject(_ context.Context, kind ServingKind, ref ObjectRef) (ServingManifest, []byte, error) {
	namespace, err := servingRefNamespace(ref, d.release.RootURI, kind)
	if err != nil {
		return nil, nil, err
	}
	payload, err := d.readExact(ref, namespace)
	if err != nil {
		return nil, nil, err
	}
	manifest, err := DecodeServingObject(payload, d.release.RootURI, kind, ref)
	if err != nil {
		return nil, nil, err
	}
	return manifest, payload, nil
}

// ReadServingPolicy parses the exact exported bytes of a compiled policy.
func (d *ServingDirectory) ReadServingPolicy(_ context.Context, ref ObjectRef) (*rosterdata.Roster, error) {
	payload, err := d.readExact(ref, policyObjectPrefix)
	if err != nil {
		return nil, err
	}
	roster, err := rosterdata.Parse(payload)
	if err != nil {
		return nil, err
	}
	roster.SHA256 = rosterdata.SHA256Hex(payload)
	return roster, nil
}

// ReadServingState is unavailable: a local release has no activation state to admit against.
func (d *ServingDirectory) ReadServingState(context.Context, ServingTarget) (ServingStateSnapshot, error) {
	return ServingStateSnapshot{}, errors.New("a local serving release has no target state")
}

// CompareAndSwapServingState is unavailable: a local release is read-only.
func (d *ServingDirectory) CompareAndSwapServingState(context.Context, ServingControlState, int64) (ServingStateSnapshot, error) {
	return ServingStateSnapshot{}, errors.New("a local serving release is read-only")
}

// ReadClassifierRuntimeBinding returns the self-hosted worker binding for the exported release only.
func (d *ServingDirectory) ReadClassifierRuntimeBinding(_ context.Context, environment Environment, releaseSHA256 string) (ResolvedClassifierRuntimeBinding, error) {
	if environment != d.binding.Binding.Environment || releaseSHA256 != d.binding.Binding.ReleaseSHA256 {
		return ResolvedClassifierRuntimeBinding{}, ErrNotFound
	}
	return d.binding, nil
}

func (d *ServingDirectory) readExact(ref ObjectRef, requiredPathPrefix string) ([]byte, error) {
	if !validDigest(ref.SHA256) || ref.Generation <= 0 {
		return nil, errors.New("immutable object reference has invalid digest or generation")
	}
	relative, found := strings.CutPrefix(ref.URI, strings.TrimRight(d.release.RootURI, "/")+"/")
	if !found || !strings.HasPrefix(relative, requiredPathPrefix) || !strings.HasSuffix(relative, "/"+ref.SHA256+".json") {
		return nil, errors.New("immutable object URI does not match its digest path")
	}
	payload, err := readLocalObject(filepath.Join(d.dir, filepath.FromSlash(relative)))
	if err != nil {
		return nil, err
	}
	if Digest(payload) != ref.SHA256 {
		return nil, fmt.Errorf("local object %s does not match its digest", relative)
	}
	return payload, nil
}

// validateSelfHostedClassifierWorkerOrigin accepts a bare HTTP(S) origin on the private
// self-hosted network. Managed bindings never pass through here.
func validateSelfHostedClassifierWorkerOrigin(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil ||
		parsed.Opaque != "" || strings.TrimRight(parsed.Path, "/") != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("invalid self-hosted classifier worker origin %q", raw)
	}
	return nil
}

func selfHostedRuntimeBinding(target ServingTarget, releaseSHA256, imageDigest, workerURL string) (ResolvedClassifierRuntimeBinding, error) {
	environment, err := target.Environment()
	if err != nil {
		return ResolvedClassifierRuntimeBinding{}, err
	}
	origin := strings.TrimRight(workerURL, "/")
	binding := ClassifierRuntimeBinding{
		SchemaVersion: ClassifierRuntimeBindingSchemaV1, Environment: environment, ReleaseSHA256: releaseSHA256,
		WorkerRevision: selfHostedClassifierRevision, WorkerURL: origin, WorkerAudience: origin, ImageDigest: imageDigest,
	}
	payload, err := json.Marshal(binding)
	if err != nil {
		return ResolvedClassifierRuntimeBinding{}, err
	}
	return ResolvedClassifierRuntimeBinding{
		Ref:     ObjectRef{URI: origin, SHA256: Digest(payload), Generation: directoryHeadRevision},
		Binding: binding,
	}, nil
}

var _ ServingStore = (*ServingDirectory)(nil)
