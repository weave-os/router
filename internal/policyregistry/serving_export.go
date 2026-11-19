package policyregistry

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

type ServingExport struct {
	Head         LaneHead  `json:"head"`
	ActivationID string    `json:"activation_id"`
	SelectionSet ObjectRef `json:"selection_set"`
	Candidate    ObjectRef `json:"candidate"`
	Package      ObjectRef `json:"package"`
}

type ServingExportStore interface {
	ServingStore
	ReadPolicyPayload(context.Context, ObjectRef) ([]byte, error)
}

func (r *Registry) ReadPolicyPayload(ctx context.Context, ref ObjectRef) ([]byte, error) {
	return r.readExact(ctx, ref, policyObjectPrefix)
}

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
	if _, err := store.ReadPolicy(ctx, policyRef); err != nil {
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
