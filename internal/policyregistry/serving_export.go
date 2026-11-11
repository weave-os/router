package policyregistry

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"weave-os/router/internal/router/hmm/rosterdata"
)

// ServingExport is what a directory export of a managed serving target pins.
type ServingExport struct {
	Head         LaneHead  `json:"head"`
	ActivationID string    `json:"activation_id"`
	SelectionSet ObjectRef `json:"selection_set"`
	Candidate    ObjectRef `json:"candidate"`
	Package      ObjectRef `json:"package"`
}

// ServingExportStore reads the serving objects an export resolves plus exact policy bytes.
type ServingExportStore interface {
	ServingStore
	ReadPolicyPayload(context.Context, ObjectRef) ([]byte, error)
}

// ReadPolicyPayload returns the exact promoted bytes of a selection policy.
func (r *Registry) ReadPolicyPayload(ctx context.Context, ref ObjectRef) ([]byte, error) {
	return r.readExact(ctx, ref, policyObjectPrefix)
}

// ExportServingTarget writes the default lane of a managed target's active selection set into
// the directory layout DirectoryRegistry serves. Managed candidates have no v1 release object,
// so the export synthesizes one from the candidate's classifier identity and the lane's effective
// policy; the policy object keeps its exact promoted bytes. Profile rosters are not exported.
func ExportServingTarget(ctx context.Context, store ServingExportStore, target ServingTarget, dir, classifierImageDigest string) (ServingExport, error) {
	environment, err := target.Environment()
	if err != nil {
		return ServingExport{}, err
	}
	state, err := store.ReadServingState(ctx, target)
	if err != nil {
		return ServingExport{}, fmt.Errorf("read serving state for %s: %w", target, err)
	}
	activation, exists := state.State.Activations[state.State.CurrentActivationID]
	if !exists || activation.WithdrawnAt != nil {
		return ServingExport{}, fmt.Errorf("serving target %s has no active selection set", target)
	}
	lanes := newLaneReader(store)
	selection, err := lanes.selectionSet(ctx, activation.SelectionSet)
	if err != nil {
		return ServingExport{}, err
	}
	candidate, err := lanes.effectiveCandidate(ctx, selection.Default)
	if err != nil {
		return ServingExport{}, err
	}
	policyRef := ObjectRef{URI: candidate.Policy.URI, SHA256: candidate.Policy.SHA256, Generation: candidate.Policy.Generation}
	roster, err := store.ReadServingPolicy(ctx, policyRef)
	if err != nil {
		return ServingExport{}, err
	}
	if err := rosterdata.ValidateCatalog(roster); err != nil {
		return ServingExport{}, err
	}
	policyPayload, err := store.ReadPolicyPayload(ctx, policyRef)
	if err != nil {
		return ServingExport{}, err
	}
	identity := candidate.Classifier.Identity
	if classifierImageDigest != "" {
		identity.ImageDigest = classifierImageDigest
	}
	activatedAt := activation.ActivatedAt.UTC().Format(time.RFC3339)
	release := Release{
		SchemaVersion: ReleaseSchemaV1,
		Classifier:    identity,
		Policy:        candidate.Policy,
		Provenance: Provenance{
			SourceRevision: candidate.Provenance.RouterRevision,
			CreatedBy:      activation.Actor,
			CreatedAt:      activatedAt,
		},
	}
	if err := release.Validate(store.RootURI()); err != nil {
		return ServingExport{}, fmt.Errorf("active candidate does not form a valid release: %w", err)
	}
	releasePayload, err := json.Marshal(release)
	if err != nil {
		return ServingExport{}, fmt.Errorf("encode exported release: %w", err)
	}
	releaseSHA256 := Digest(releasePayload)
	head := LaneHead{
		SchemaVersion:          LaneHeadSchemaV1,
		Environment:            environment,
		Lane:                   LaneStable,
		ReleaseURI:             store.RootURI() + "/" + releaseObjectPrefix + releaseSHA256 + ".json",
		ReleaseSHA256:          releaseSHA256,
		ReleaseGeneration:      directoryHeadRevision,
		ClassifierRevisionURL:  selection.Default.Binding.Classifier.URL,
		ClassifierRevisionName: selection.Default.Binding.Classifier.Name,
		PromotedBy:             activation.Actor,
		PromotedAt:             activatedAt,
		Reason:                 fmt.Sprintf("exported from serving target %s activation %s", target, activation.ID),
	}
	if err := WriteDirectoryRelease(dir, head, releasePayload, policyPayload); err != nil {
		return ServingExport{}, err
	}
	return ServingExport{
		Head:         head,
		ActivationID: activation.ID,
		SelectionSet: activation.SelectionSet,
		Candidate:    selection.Default.Candidate,
		Package:      candidate.Classifier.Package,
	}, nil
}

// LocalServingExport is what a self-hosted export of a managed target's V4 selection pins.
type LocalServingExport struct {
	Release           LocalServingRelease `json:"release"`
	ActivationID      string              `json:"activation_id"`
	ClassifierRelease ObjectRef           `json:"classifier_release"`
	Objects           int                 `json:"objects"`
}

// recordingServingStore keeps the exact bytes of every serving object and policy a read resolves.
type recordingServingStore struct {
	ServingExportStore
	objects map[string][]byte
}

func (s *recordingServingStore) ReadServingObject(ctx context.Context, kind ServingKind, ref ObjectRef) (ServingManifest, []byte, error) {
	manifest, payload, err := s.ServingExportStore.ReadServingObject(ctx, kind, ref)
	if err == nil {
		s.objects[ref.URI] = payload
	}
	return manifest, payload, err
}

func (s *recordingServingStore) ReadServingPolicy(ctx context.Context, ref ObjectRef) (*rosterdata.Roster, error) {
	roster, err := s.ServingExportStore.ReadServingPolicy(ctx, ref)
	if err != nil {
		return nil, err
	}
	payload, err := s.ReadPolicyPayload(ctx, ref)
	if err != nil {
		return nil, err
	}
	s.objects[ref.URI] = payload
	return roster, nil
}

// ExportServingTargetV4 writes the exact closure of a target's active V4 selection set, every
// lane's prepared selection, into the layout ServingDirectory serves. It never exports the
// managed runtime binding: a self-hosted Router binds the release to its local worker.
func ExportServingTargetV4(ctx context.Context, store ServingExportStore, target ServingTarget, dir string) (LocalServingExport, error) {
	state, err := store.ReadServingState(ctx, target)
	if err != nil {
		return LocalServingExport{}, fmt.Errorf("read serving state for %s: %w", target, err)
	}
	activation, exists := state.State.Activations[state.State.CurrentActivationID]
	if !exists || activation.WithdrawnAt != nil {
		return LocalServingExport{}, fmt.Errorf("serving target %s has no active selection set", target)
	}
	recorder := &recordingServingStore{ServingExportStore: store, objects: make(map[string][]byte)}
	selection, err := readServing[*SelectionSetV4](ctx, recorder, ServingSelectionSet, activation.SelectionSet)
	if err != nil {
		return LocalServingExport{}, fmt.Errorf("serving target %s does not serve a V4 selection set: %w", target, err)
	}
	view := selection.View(activation.SelectionSet)
	lanes := map[string]ServingSelection{"": view.Default}
	for key, lane := range view.Profiles {
		lanes[key] = lane
	}
	for profileKey, lane := range lanes {
		if _, err := ReadPreparedSelection(ctx, recorder, target, profileKey, lane); err != nil {
			return LocalServingExport{}, fmt.Errorf("prepare exported lane %q: %w", profileKey, err)
		}
	}
	root := strings.TrimRight(store.RootURI(), "/")
	for uri, payload := range recorder.objects {
		relative, found := strings.CutPrefix(uri, root+"/")
		if !found {
			return LocalServingExport{}, fmt.Errorf("exported object %s is outside the registry root", uri)
		}
		path := filepath.Join(dir, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return LocalServingExport{}, err
		}
		if err := os.WriteFile(path, payload, 0o644); err != nil {
			return LocalServingExport{}, err
		}
	}
	release := LocalServingRelease{SchemaVersion: LocalServingReleaseSchemaV1, RootURI: root, Target: target, SelectionSet: activation.SelectionSet}
	manifest, err := json.Marshal(release)
	if err != nil {
		return LocalServingExport{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, localServingReleaseFile), manifest, 0o644); err != nil {
		return LocalServingExport{}, err
	}
	exported, err := NewServingDirectory(dir, "http://classifier-worker")
	if err != nil {
		return LocalServingExport{}, fmt.Errorf("exported release does not load: %w", err)
	}
	for profileKey, lane := range lanes {
		if _, err := ReadPreparedSelection(ctx, exported, target, profileKey, lane); err != nil {
			return LocalServingExport{}, fmt.Errorf("exported lane %q does not prepare: %w", profileKey, err)
		}
	}
	return LocalServingExport{Release: release, ActivationID: activation.ID, ClassifierRelease: selection.Classifier.Release, Objects: len(recorder.objects)}, nil
}
