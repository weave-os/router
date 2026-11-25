package policyregistry

import (
	"context"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

var contentAddressedObjectName = regexp.MustCompile(`^[0-9a-f]{64}\.json$`)

func carrySelfHostedHistory(ctx context.Context, request SelfHostedExportRequest, graph selfHostedGraph) (*ServingControlState, error) {
	if request.PreviousDir == "" {
		return nil, nil
	}
	store, err := NewDirectoryServingStore(request.PreviousDir)
	if err != nil {
		return nil, fmt.Errorf("open previous self-hosted export: %w", err)
	}
	if store.RootURI() != graph.root {
		return nil, fmt.Errorf("previous export root %s differs from %s", store.RootURI(), graph.root)
	}
	snapshot, err := store.ReadServingState(ctx, request.Target)
	if err != nil {
		return nil, fmt.Errorf("read previous self-hosted serving state: %w", err)
	}
	statePath, err := DirectoryServingStatePath(request.Target)
	if err != nil {
		return nil, err
	}
	regenerated := map[string]struct{}{statePath: {}, directoryServingRootFile: {}, SelfHostedClassifierReleasePath: {}, SelfHostedClassifierSelectionPath: {}}
	previousRoot, err := os.OpenRoot(request.PreviousDir)
	if err != nil {
		return nil, fmt.Errorf("open previous self-hosted export: %w", err)
	}
	defer func() { _ = previousRoot.Close() }()
	previousFS := previousRoot.FS()
	err = fs.WalkDir(previousFS, ".", func(relative string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		if _, skip := regenerated[relative]; skip {
			return nil
		}
		payload, err := fs.ReadFile(previousFS, relative)
		if err != nil {
			return err
		}
		if name := path.Base(relative); !strings.HasPrefix(relative, "runtime_bindings/") && contentAddressedObjectName.MatchString(name) && Digest(payload)+".json" != name {
			return fmt.Errorf("previous export object %s does not match its digest", relative)
		}
		graph.objects[relative] = payload
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("copy previous self-hosted export: %w", err)
	}
	return &snapshot.State, nil
}

func selfHostedServingState(previous *ServingControlState, target ServingTarget, next Activation) ServingControlState {
	state := ServingControlState{SchemaVersion: ServingControlStateV1, Target: target, Sequence: 1, Activations: map[string]Activation{}}
	if previous != nil {
		current := previous.Activations[previous.CurrentActivationID]
		if current.SelectionSet == next.SelectionSet {
			return *previous
		}
		state.Sequence = previous.Sequence + 1
		state.Activations = maps.Clone(previous.Activations)
		if next.ActivatedAt.Before(current.ActivatedAt) {
			next.ActivatedAt = current.ActivatedAt
		}
	}
	next.Sequence = state.Sequence
	next.ID = uuid.NewSHA1(selfHostedActivationNamespace, fmt.Appendf(nil, "%s/%d", next.SelectionSet.SHA256, next.Sequence)).String()
	retiredAt := next.ActivatedAt
	for id, retired := range state.Activations {
		if retired.SupersededAt == nil {
			retired.SupersededAt = &retiredAt
		}
		if retired.WithdrawnAt == nil {
			retired.WithdrawnAt = &retiredAt
			retired.ReplacementID = next.ID
		}
		state.Activations[id] = retired
	}
	state.CurrentActivationID = next.ID
	state.Activations[next.ID] = next
	return state
}
