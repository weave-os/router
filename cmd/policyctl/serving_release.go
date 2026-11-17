package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"weave-os/router/internal/policyregistry"
)

const commandPublishRelease commandName = "publish-release"

const placeholderGeneration = 1

type servingManifestFile struct {
	kind    policyregistry.ServingKind
	payload []byte
	digest  string
}

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
	defer func() { _ = registry.Close() }()
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

func placeholderRef(root, digest string) policyregistry.ObjectRef {
	return policyregistry.ObjectRef{URI: strings.TrimRight(root, "/") + "/artifacts/" + digest + ".json", SHA256: digest, Generation: placeholderGeneration}
}

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
		stated, err := statedReference(lane, "candidate")
		if err != nil {
			return nil, fmt.Errorf("selection set lane %q: %w", key, err)
		}
		if stated.SHA256 != "" && stated.SHA256 != digest {
			continue
		}
		if err := matchesPublished(stated, candidate, "candidate"); err != nil {
			return nil, fmt.Errorf("selection set lane %q: %w", key, err)
		}
		lane["candidate"] = reference
		filled = true
	}
	if !filled {
		return nil, fmt.Errorf("no selection set lane references the candidate %s", digest)
	}
	return json.Marshal(document)
}

func fillProposalSources(document map[string]any, selectionSet, candidate policyregistry.ObjectRef) ([]byte, error) {
	for field, published := range map[string]policyregistry.ObjectRef{"selection_set": selectionSet, "source_candidate": candidate} {
		stated, err := statedReference(document, field)
		if err != nil {
			return nil, fmt.Errorf("proposal: %w", err)
		}
		if stated.SHA256 != "" && stated.SHA256 != published.SHA256 {
			return nil, fmt.Errorf("proposal names %s %s, but this release publishes %s", field, stated.SHA256, published.SHA256)
		}
		if err := matchesPublished(stated, published, field); err != nil {
			return nil, fmt.Errorf("proposal: %w", err)
		}
		reference, err := referenceDocument(published)
		if err != nil {
			return nil, err
		}
		document[field] = reference
	}
	return json.Marshal(document)
}

func selectionSetLanes(document map[string]any) (map[string]map[string]any, error) {
	lanes := map[string]map[string]any{}
	if document["schema_version"] == string(policyregistry.ServingSelectionSetV3) {
		code, ok := document["code"].(map[string]any)
		if !ok {
			return nil, errors.New("selection set requires a code binding")
		}
		return map[string]map[string]any{"code": code}, nil
	}
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

func matchesPublished(stated, published policyregistry.ObjectRef, field string) error {
	if stated.URI != "" && stated.URI != published.URI {
		return fmt.Errorf("%s reference names %s, but this release publishes %s", field, stated.URI, published.URI)
	}
	return nil
}

func statedReference(document map[string]any, field string) (policyregistry.ObjectRef, error) {
	value, present := document[field]
	if !present || value == nil {
		return policyregistry.ObjectRef{}, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return policyregistry.ObjectRef{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var reference policyregistry.ObjectRef
	if err := decoder.Decode(&reference); err != nil {
		return policyregistry.ObjectRef{}, fmt.Errorf("%s reference: %w", field, err)
	}
	return reference, nil
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
