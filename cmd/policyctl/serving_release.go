package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"weave-os/router/internal/policyregistry"
)

const commandPublishRelease commandName = "publish-release"

// placeholderGeneration stands in for the generation GCS assigns at write time, so a dry run can
// run every structural check on manifests whose real references do not exist yet.
const placeholderGeneration = 1

// servingManifestFile is one decoded member of the release, kept with the exact bytes its digest
// and publication are keyed on.
type servingManifestFile struct {
	kind    policyregistry.ServingKind
	payload []byte
	digest  string
}

// servingManifestDigest reports the digest a publish of these bytes would use. The selection set
// and the proposal are only digestible once the objects they reference exist, so a dry run
// reports that they validated instead of inventing a digest.
type servingManifestDigest struct {
	Kind      policyregistry.ServingKind `json:"kind"`
	SHA256    string                     `json:"sha256,omitempty"`
	Validated bool                       `json:"validated,omitempty"`
}

type servingReleaseDigests struct {
	Candidate    servingManifestDigest `json:"candidate"`
	SelectionSet servingManifestDigest `json:"selection_set"`
	Proposal     servingManifestDigest `json:"proposal"`
}

type servingReleaseRefs struct {
	Candidate    policyregistry.ObjectRef `json:"candidate"`
	SelectionSet policyregistry.ObjectRef `json:"selection_set"`
	Proposal     policyregistry.ObjectRef `json:"proposal"`
}

// servingPublishRelease publishes the candidate, its selection set and its proposal through the
// same create-only publish path a single `publish` uses, in that order. An ObjectRef carries the
// generation the registry assigns at write time, which no caller can know for an object that does
// not exist yet, so the selection set's lane candidates and the proposal's selection set and
// source candidate are filled in from what was actually published rather than transcribed by the
// caller. Filling is deterministic, so re-running a release re-derives the same bytes and the
// create-only path reports the existing objects without writing. Nothing is applied or activated.
func servingPublishRelease(ctx context.Context, dependencies servingDependencies, root, candidatePath, selectionSetPath, proposalPath string, dryRun bool) error {
	if candidatePath == "" || selectionSetPath == "" || proposalPath == "" {
		return errors.New("serving publish-release requires --candidate, --selection-set and --proposal")
	}
	candidate, err := readServingManifestFile(candidatePath, root, policyregistry.ServingCandidate)
	if err != nil {
		return err
	}
	selectionSetDocument, err := readServingDocument(selectionSetPath, policyregistry.ServingSelectionSet)
	if err != nil {
		return err
	}
	proposalDocument, err := readServingDocument(proposalPath, policyregistry.ServingProposal)
	if err != nil {
		return err
	}
	if dryRun {
		return dryRunServingRelease(dependencies, root, candidate, selectionSetDocument, proposalDocument)
	}
	registry, err := dependencies.openRegistry(ctx, root)
	if err != nil {
		return err
	}
	defer registry.Close()
	candidateRef, err := registry.PublishServingManifest(ctx, candidate.kind, candidate.payload)
	if err != nil {
		return err
	}
	selectionSet, err := fillLaneCandidates(selectionSetDocument, candidate.digest, candidateRef)
	if err != nil {
		return err
	}
	selectionSetRef, err := publishFilledManifest(ctx, registry, policyregistry.ServingSelectionSet, selectionSet)
	if err != nil {
		return err
	}
	proposal, err := fillProposalSources(proposalDocument, selectionSetRef, candidateRef)
	if err != nil {
		return err
	}
	proposalRef, err := publishFilledManifest(ctx, registry, policyregistry.ServingProposal, proposal)
	if err != nil {
		return err
	}
	return dependencies.writeOutput(servingReleaseRefs{Candidate: candidateRef, SelectionSet: selectionSetRef, Proposal: proposalRef})
}

// dryRunServingRelease runs every check a release performs except the ones that need the objects
// to exist: the filled references carry a placeholder generation, so only the candidate's digest
// is the digest the release would publish under.
func dryRunServingRelease(dependencies servingDependencies, root string, candidate servingManifestFile, selectionSetDocument, proposalDocument map[string]any) error {
	candidatePlaceholder := placeholderRef(root, candidate.digest)
	selectionSet, err := fillLaneCandidates(selectionSetDocument, candidate.digest, candidatePlaceholder)
	if err != nil {
		return err
	}
	if err := decodeFilledManifest(policyregistry.ServingSelectionSet, selectionSet, root); err != nil {
		return err
	}
	proposal, err := fillProposalSources(proposalDocument, placeholderRef(root, policyregistry.Digest(selectionSet)), candidatePlaceholder)
	if err != nil {
		return err
	}
	if err := decodeFilledManifest(policyregistry.ServingProposal, proposal, root); err != nil {
		return err
	}
	return dependencies.writeOutput(servingReleaseDigests{
		Candidate:    servingManifestDigest{Kind: candidate.kind, SHA256: candidate.digest},
		SelectionSet: servingManifestDigest{Kind: policyregistry.ServingSelectionSet, Validated: true},
		Proposal:     servingManifestDigest{Kind: policyregistry.ServingProposal, Validated: true},
	})
}

// readServingManifestFile applies every pre-write check a single publish performs.
func readServingManifestFile(path, root string, kind policyregistry.ServingKind) (servingManifestFile, error) {
	payload, err := readServingPayload(path)
	if err != nil {
		return servingManifestFile{}, err
	}
	if _, err := policyregistry.DecodePublishableServingManifest(payload, root, kind); err != nil {
		return servingManifestFile{}, fmt.Errorf("%s manifest: %w", kind, err)
	}
	return servingManifestFile{kind: kind, payload: payload, digest: policyregistry.Digest(payload)}, nil
}

// readServingDocument reads a manifest whose references are still to be filled in. It is decoded
// as a document rather than a manifest because it cannot validate until it is complete; numbers
// keep their literal text so a generation is never rounded through float64.
func readServingDocument(path string, kind policyregistry.ServingKind) (map[string]any, error) {
	payload, err := readServingPayload(path)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	document := map[string]any{}
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("%s manifest: %w", kind, err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s manifest: trailing JSON after the manifest", kind)
	}
	return document, nil
}

func readServingPayload(path string) ([]byte, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read serving manifest: %w", err)
	}
	return bytes.TrimSpace(payload), nil
}

func publishFilledManifest(ctx context.Context, registry servingRegistry, kind policyregistry.ServingKind, payload []byte) (policyregistry.ObjectRef, error) {
	ref, err := registry.PublishServingManifest(ctx, kind, payload)
	if err != nil {
		return policyregistry.ObjectRef{}, fmt.Errorf("%s manifest: %w", kind, err)
	}
	return ref, nil
}

func decodeFilledManifest(kind policyregistry.ServingKind, payload []byte, root string) error {
	if _, err := policyregistry.DecodePublishableServingManifest(payload, root, kind); err != nil {
		return fmt.Errorf("%s manifest: %w", kind, err)
	}
	return nil
}

// placeholderRef is the reference an object with these bytes will have, except for the generation
// the registry assigns when it is created.
func placeholderRef(root, digest string) policyregistry.ObjectRef {
	return policyregistry.ObjectRef{URI: root + "/artifacts/" + digest + ".json", SHA256: digest, Generation: placeholderGeneration}
}

// fillLaneCandidates binds every lane that names the published candidate — by digest, or by
// naming no digest at all — to its exact reference. Lanes pinned to another, already published
// candidate are left alone, and a selection set that names the candidate nowhere is rejected: it
// belongs to a different release.
func fillLaneCandidates(document map[string]any, digest string, candidate policyregistry.ObjectRef) ([]byte, error) {
	lanes, err := selectionSetLanes(document)
	if err != nil {
		return nil, err
	}
	reference, err := referenceDocument(candidate)
	if err != nil {
		return nil, err
	}
	filled := false
	for key, lane := range lanes {
		stated, err := statedDigest(lane, "candidate")
		if err != nil {
			return nil, fmt.Errorf("selection set lane %q: %w", key, err)
		}
		if stated != "" && stated != digest {
			continue
		}
		lane["candidate"] = reference
		filled = true
	}
	if !filled {
		return nil, fmt.Errorf("no selection set lane references the candidate %s", digest)
	}
	return json.Marshal(document)
}

// fillProposalSources binds the proposal to the objects published alongside it. A proposal that
// names a different object is a proposal for a different release, not one to rewrite.
func fillProposalSources(document map[string]any, selectionSet, candidate policyregistry.ObjectRef) ([]byte, error) {
	for field, published := range map[string]policyregistry.ObjectRef{"selection_set": selectionSet, "source_candidate": candidate} {
		stated, err := statedDigest(document, field)
		if err != nil {
			return nil, fmt.Errorf("proposal: %w", err)
		}
		if stated != "" && stated != published.SHA256 {
			return nil, fmt.Errorf("proposal names %s %s, but this release publishes %s", field, stated, published.SHA256)
		}
		reference, err := referenceDocument(published)
		if err != nil {
			return nil, err
		}
		document[field] = reference
	}
	return json.Marshal(document)
}

// selectionSetLanes returns the default lane and every profile lane by key. Anything that is not
// shaped like a lane is left to the decoder of the filled bytes to reject.
func selectionSetLanes(document map[string]any) (map[string]map[string]any, error) {
	lanes := map[string]map[string]any{}
	if lane, ok := document["default"].(map[string]any); ok {
		lanes["default"] = lane
	}
	profiles, present := document["profiles"]
	if present {
		keyed, ok := profiles.(map[string]any)
		if !ok {
			return nil, errors.New("selection set profiles must be an object")
		}
		for key, lane := range keyed {
			typed, ok := lane.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("selection set lane %q must be an object", key)
			}
			lanes[key] = typed
		}
	}
	if len(lanes) == 0 {
		return nil, errors.New("selection set declares no lanes")
	}
	return lanes, nil
}

// statedDigest reads the digest the caller expects the field's object to have. An absent field or
// an absent digest means the caller left the reference to this command.
func statedDigest(document map[string]any, field string) (string, error) {
	value, present := document[field]
	if !present || value == nil {
		return "", nil
	}
	reference, ok := value.(map[string]any)
	if !ok {
		return "", fmt.Errorf("%s must be an object reference", field)
	}
	digest, present := reference["sha256"]
	if !present || digest == nil {
		return "", nil
	}
	typed, ok := digest.(string)
	if !ok {
		return "", fmt.Errorf("%s sha256 must be a string", field)
	}
	return typed, nil
}

func referenceDocument(ref policyregistry.ObjectRef) (map[string]any, error) {
	encoded, err := json.Marshal(ref)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	document := map[string]any{}
	if err := decoder.Decode(&document); err != nil {
		return nil, err
	}
	return document, nil
}
