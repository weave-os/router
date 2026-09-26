package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"

	"weave-os/router/internal/policyregistry"
)

const commandPublishRelease commandName = "publish-release"

// defaultLaneKey names the selection set's default lane in cross-reference errors.
const defaultLaneKey = "default"

// servingManifestFile is one decoded member of the release triple, kept with the exact bytes
// its digest and publication are keyed on.
type servingManifestFile struct {
	kind     policyregistry.ServingKind
	payload  []byte
	digest   string
	manifest policyregistry.ServingManifest
}

// servingManifestDigest reports the digest a publish of these bytes would use.
type servingManifestDigest struct {
	Kind   policyregistry.ServingKind `json:"kind"`
	SHA256 string                     `json:"sha256"`
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
// same create-only publish path a single `publish` uses, in that order. The manifests are
// content-addressed, so the caller states the references it expects and this verb verifies them
// rather than rewriting the files: a reference that does not match the object actually published
// fails before the next manifest is written. Nothing is applied, activated or CAS'd here.
func servingPublishRelease(ctx context.Context, dependencies servingDependencies, root, candidatePath, selectionSetPath, proposalPath string, dryRun bool) error {
	if candidatePath == "" || selectionSetPath == "" || proposalPath == "" {
		return errors.New("serving publish-release requires --candidate, --selection-set and --proposal")
	}
	candidate, err := readServingManifestFile(candidatePath, root, policyregistry.ServingCandidate)
	if err != nil {
		return err
	}
	selectionSet, err := readServingManifestFile(selectionSetPath, root, policyregistry.ServingSelectionSet)
	if err != nil {
		return err
	}
	proposal, err := readServingManifestFile(proposalPath, root, policyregistry.ServingProposal)
	if err != nil {
		return err
	}
	set, ok := selectionSet.manifest.(*policyregistry.SelectionSetV2)
	if !ok {
		return errors.New("serving publish-release requires a v2 selection set")
	}
	view, ok := proposal.manifest.(*policyregistry.DeploymentProposalV2)
	if !ok {
		return errors.New("serving publish-release requires a v2 proposal")
	}
	if err := verifyLaneCandidates(set, candidate.digest, nil); err != nil {
		return err
	}
	if err := verifyProposalSources(view, selectionSet.digest, candidate.digest, nil, nil); err != nil {
		return err
	}
	if dryRun {
		return dependencies.writeOutput(servingReleaseDigests{
			Candidate:    servingManifestDigest{candidate.kind, candidate.digest},
			SelectionSet: servingManifestDigest{selectionSet.kind, selectionSet.digest},
			Proposal:     servingManifestDigest{proposal.kind, proposal.digest},
		})
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
	if err := verifyLaneCandidates(set, candidate.digest, &candidateRef); err != nil {
		return err
	}
	if err := verifyProposalSources(view, selectionSet.digest, candidate.digest, nil, &candidateRef); err != nil {
		return err
	}
	selectionSetRef, err := registry.PublishServingManifest(ctx, selectionSet.kind, selectionSet.payload)
	if err != nil {
		return err
	}
	if err := verifyProposalSources(view, selectionSet.digest, candidate.digest, &selectionSetRef, &candidateRef); err != nil {
		return err
	}
	proposalRef, err := registry.PublishServingManifest(ctx, proposal.kind, proposal.payload)
	if err != nil {
		return err
	}
	return dependencies.writeOutput(servingReleaseRefs{Candidate: candidateRef, SelectionSet: selectionSetRef, Proposal: proposalRef})
}

// readServingManifestFile applies every pre-write check a single publish performs.
func readServingManifestFile(path, root string, kind policyregistry.ServingKind) (servingManifestFile, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return servingManifestFile{}, fmt.Errorf("read serving manifest: %w", err)
	}
	payload = bytes.TrimSpace(payload)
	manifest, err := policyregistry.DecodePublishableServingManifest(payload, root, kind)
	if err != nil {
		return servingManifestFile{}, fmt.Errorf("%s manifest: %w", kind, err)
	}
	return servingManifestFile{kind: kind, payload: payload, digest: policyregistry.Digest(payload), manifest: manifest}, nil
}

// verifyLaneCandidates requires the selection set to name the candidate being published, and
// every lane that names its digest to name the exact reference it was published under. Lanes
// pinned to a different, already published candidate are left alone. Before the candidate is
// published its generation is unknown, so a nil reference checks digests only.
func verifyLaneCandidates(set *policyregistry.SelectionSetV2, digest string, published *policyregistry.ObjectRef) error {
	matched := false
	lanes := map[string]policyregistry.ServingLane{defaultLaneKey: set.Default}
	for key, lane := range set.Profiles {
		lanes[key] = lane
	}
	for key, lane := range lanes {
		if lane.Candidate.SHA256 != digest {
			continue
		}
		matched = true
		if published != nil && lane.Candidate != *published {
			return fmt.Errorf("selection set lane %q names candidate %s at generation %d, but the candidate published as %s at generation %d", key, lane.Candidate.URI, lane.Candidate.Generation, published.URI, published.Generation)
		}
	}
	if !matched {
		return fmt.Errorf("no selection set lane references the candidate %s", digest)
	}
	return nil
}

// verifyProposalSources binds the proposal to the objects published alongside it. A nil
// reference checks the digest only, which is all that is known before that object is published.
func verifyProposalSources(proposal *policyregistry.DeploymentProposalV2, selectionSetDigest, candidateDigest string, selectionSet, candidate *policyregistry.ObjectRef) error {
	if proposal.SelectionSet.SHA256 != selectionSetDigest {
		return fmt.Errorf("proposal names selection set %s, but --selection-set publishes %s", proposal.SelectionSet.SHA256, selectionSetDigest)
	}
	if selectionSet != nil && proposal.SelectionSet != *selectionSet {
		return fmt.Errorf("proposal names selection set %s at generation %d, but the selection set published as %s at generation %d", proposal.SelectionSet.URI, proposal.SelectionSet.Generation, selectionSet.URI, selectionSet.Generation)
	}
	if proposal.SourceCandidate.SHA256 != candidateDigest {
		return fmt.Errorf("proposal names source candidate %s, but --candidate publishes %s", proposal.SourceCandidate.SHA256, candidateDigest)
	}
	if candidate != nil && proposal.SourceCandidate != *candidate {
		return fmt.Errorf("proposal names source candidate %s at generation %d, but the candidate published as %s at generation %d", proposal.SourceCandidate.URI, proposal.SourceCandidate.Generation, candidate.URI, candidate.Generation)
	}
	return nil
}
