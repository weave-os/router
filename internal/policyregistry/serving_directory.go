package policyregistry

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"weave-os/router/internal/router/rosterdata"
)

const (
	directoryServingRootFile   = "registry_root"
	directoryServingGeneration = 1
)

type DirectoryServingStore struct {
	dir     string
	rootURI string
}

func NewDirectoryServingStore(dir string) (*DirectoryServingStore, error) {
	payload, err := readLocalObject(filepath.Join(dir, directoryServingRootFile))
	if err != nil {
		return nil, err
	}
	rootURI := strings.TrimRight(strings.TrimSpace(string(payload)), "/")
	if err := validateDirectoryServingRoot(rootURI); err != nil {
		return nil, err
	}
	return &DirectoryServingStore{dir: dir, rootURI: rootURI}, nil
}

func validateDirectoryServingRoot(rootURI string) error {
	parsed, err := url.Parse(rootURI)
	if err != nil || parsed.Scheme != "gs" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("local serving registry root %q must be a gs:// URI", rootURI)
	}
	return nil
}

func (s *DirectoryServingStore) RootURI() string { return s.rootURI }

func (s *DirectoryServingStore) ReadServingObject(_ context.Context, kind ServingKind, ref ObjectRef) (ServingManifest, []byte, error) {
	namespace, err := ServingRefNamespace(ref, s.rootURI, kind)
	if err != nil {
		return nil, nil, err
	}
	payload, err := s.readPinned(ref, namespace)
	if err != nil {
		return nil, nil, err
	}
	manifest, err := DecodeServingObject(payload, s.rootURI, kind, ref)
	if err != nil {
		return nil, nil, err
	}
	return manifest, payload, nil
}

func (s *DirectoryServingStore) ReadServingPolicy(_ context.Context, ref ObjectRef) (*rosterdata.Roster, error) {
	payload, err := s.readPinned(ref, PolicyObjectPrefix)
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

func (s *DirectoryServingStore) ReadServingState(_ context.Context, target ServingTarget) (ServingStateSnapshot, error) {
	relative, err := DirectoryServingStatePath(target)
	if err != nil {
		return ServingStateSnapshot{}, err
	}
	payload, err := readLocalObject(filepath.Join(s.dir, filepath.FromSlash(relative)))
	if err != nil {
		return ServingStateSnapshot{}, err
	}
	var state ServingControlState
	if err := StrictDecode(payload, &state); err != nil {
		return ServingStateSnapshot{}, err
	}
	if err := state.Validate(s.rootURI, target); err != nil {
		return ServingStateSnapshot{}, err
	}
	return ServingStateSnapshot{State: state, Generation: directoryServingGeneration}, nil
}

func (s *DirectoryServingStore) CompareAndSwapServingState(context.Context, ServingControlState, int64) (ServingStateSnapshot, error) {
	return ServingStateSnapshot{}, errors.New("local serving registry is read-only")
}

func (s *DirectoryServingStore) ReadClassifierRuntimeBinding(_ context.Context, environment Environment, releaseSHA256 string) (ResolvedClassifierRuntimeBinding, error) {
	if !ValidDigest(releaseSHA256) {
		return ResolvedClassifierRuntimeBinding{}, errors.New("classifier runtime binding requires a release digest")
	}
	payload, err := readLocalObject(filepath.Join(s.dir, filepath.FromSlash(ClassifierRuntimeBindingRelativeName(environment, releaseSHA256))))
	if err != nil {
		return ResolvedClassifierRuntimeBinding{}, err
	}
	binding, err := DecodeLocalClassifierRuntimeBinding(payload, environment, releaseSHA256)
	if err != nil {
		return ResolvedClassifierRuntimeBinding{}, err
	}
	return ResolvedClassifierRuntimeBinding{
		Ref:     ObjectRef{URI: ClassifierRuntimeBindingURI(s.rootURI, environment, releaseSHA256), SHA256: Digest(payload), Generation: directoryServingGeneration},
		Binding: binding,
	}, nil
}

func DirectoryServingStatePath(target ServingTarget) (string, error) {
	environment, err := target.Environment()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("state/%s/%s.json", environment, target), nil
}

func DirectoryServingRuntimeBindingPath(environment Environment, releaseSHA256 string) string {
	return ClassifierRuntimeBindingRelativeName(environment, releaseSHA256)
}

func (s *DirectoryServingStore) readPinned(ref ObjectRef, namespace string) ([]byte, error) {
	if ref.Generation != directoryServingGeneration {
		return nil, fmt.Errorf("local serving registry holds generation %d, not %d", directoryServingGeneration, ref.Generation)
	}
	return readDirectoryObject(s.dir, s.rootURI, ref, namespace)
}

func ReadDirectoryPolicyPayload(dir, rootURI string, ref ObjectRef) ([]byte, error) {
	if ref.Generation != directoryServingGeneration {
		return nil, fmt.Errorf("local serving registry holds generation %d, not %d", directoryServingGeneration, ref.Generation)
	}
	return readDirectoryObject(dir, rootURI, ref, PolicyObjectPrefix)
}
