package policyregistry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"weave-os/router/internal/router/hmm/rosterdata"
)

const (
	directoryHeadFile     = "head.json"
	releaseObjectPrefix   = "router_policy/v1/releases/sha256/"
	policyObjectPrefix    = "router_policy/v1/policies/sha256/"
	directoryHeadRevision = 1
)

// DirectoryRegistry serves one promoted lane head and the immutable objects it
// references from a local copy of the registry layout. The objects are the exact
// bytes promoted to GCS, so their digests and embedded URIs still validate; only
// the classifier endpoint is rebound to the locally running sidecar.
type DirectoryRegistry struct {
	dir     string
	rootURI string
	head    LaneHead
}

// NewDirectoryRegistry loads dir/head.json and, when classifierURL is set, points
// the head at that sidecar instead of the promoted managed revision.
func NewDirectoryRegistry(dir, classifierURL string) (*DirectoryRegistry, error) {
	payload, err := readLocalObject(filepath.Join(dir, directoryHeadFile))
	if err != nil {
		return nil, err
	}
	var probe LaneHead
	if err := strictDecode(payload, &probe); err != nil {
		return nil, fmt.Errorf("decode local lane head: %w", err)
	}
	rootURI, found := strings.CutSuffix(probe.ReleaseURI, "/"+releaseObjectPrefix+probe.ReleaseSHA256+".json")
	if !found || rootURI == "" {
		return nil, errors.New("local lane head release URI is not content-addressed")
	}
	head, err := DecodeLaneHead(payload, rootURI, probe.Environment, probe.Lane)
	if err != nil {
		return nil, err
	}
	if classifierURL = strings.TrimSpace(classifierURL); classifierURL != "" {
		parsed, parseErr := url.Parse(classifierURL)
		if parseErr != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
			return nil, fmt.Errorf("invalid local classifier URL %q", classifierURL)
		}
		head.ClassifierRevisionURL = strings.TrimRight(classifierURL, "/")
	}
	return &DirectoryRegistry{dir: dir, rootURI: rootURI, head: head}, nil
}

// Environment is the environment the baked head was promoted in.
func (r *DirectoryRegistry) Environment() Environment { return r.head.Environment }

// Lane is the lane the baked head was promoted in.
func (r *DirectoryRegistry) Lane() Lane { return r.head.Lane }

// RootURI is the registry root the baked objects were published under.
func (r *DirectoryRegistry) RootURI() string { return r.rootURI }

// ReadHead returns the baked head; it never changes for the life of the process.
func (r *DirectoryRegistry) ReadHead(_ context.Context, environment Environment, lane Lane) (HeadSnapshot, error) {
	if environment != r.head.Environment || lane != r.head.Lane {
		return HeadSnapshot{}, ErrNotFound
	}
	return HeadSnapshot{Head: r.head, Generation: directoryHeadRevision}, nil
}

// ReadRelease reads and digest-verifies a baked release manifest.
func (r *DirectoryRegistry) ReadRelease(_ context.Context, ref ObjectRef) (Release, error) {
	payload, err := r.readExact(ref, releaseObjectPrefix)
	if err != nil {
		return Release{}, err
	}
	return DecodeRelease(payload, r.rootURI)
}

// ReadPolicy reads, digest-verifies and fully validates a baked selection policy.
func (r *DirectoryRegistry) ReadPolicy(_ context.Context, ref ObjectRef) (*rosterdata.Roster, error) {
	payload, err := r.readExact(ref, policyObjectPrefix)
	if err != nil {
		return nil, err
	}
	roster, err := rosterdata.Parse(payload)
	if err != nil {
		return nil, err
	}
	roster.SHA256 = rosterdata.SHA256Hex(payload)
	if err := rosterdata.ValidateCatalog(roster); err != nil {
		return nil, err
	}
	return roster, nil
}

func (r *DirectoryRegistry) readExact(ref ObjectRef, requiredPathPrefix string) ([]byte, error) {
	if !validDigest(ref.SHA256) {
		return nil, errors.New("immutable object reference has an invalid digest")
	}
	relative, found := strings.CutPrefix(ref.URI, r.rootURI+"/")
	if !found || relative != requiredPathPrefix+ref.SHA256+".json" {
		return nil, errors.New("immutable object URI does not match its digest path")
	}
	payload, err := readLocalObject(filepath.Join(r.dir, filepath.FromSlash(relative)))
	if err != nil {
		return nil, err
	}
	if Digest(payload) != ref.SHA256 {
		return nil, fmt.Errorf("local policy object %s does not match its digest", relative)
	}
	return payload, nil
}

func readLocalObject(path string) ([]byte, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, path)
	}
	if err != nil {
		return nil, fmt.Errorf("open local policy object: %w", err)
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, maxPolicyObjectBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read local policy object: %w", err)
	}
	if len(payload) > maxPolicyObjectBytes {
		return nil, fmt.Errorf("local policy object exceeds %d bytes", maxPolicyObjectBytes)
	}
	return payload, nil
}

// WriteDirectoryRelease lays out a lane head and the exact bytes of its release and
// policy objects so NewDirectoryRegistry can serve them.
func WriteDirectoryRelease(dir string, head LaneHead, releasePayload, policyPayload []byte) error {
	if Digest(releasePayload) != head.ReleaseSHA256 {
		return errors.New("release bytes do not match the lane head digest")
	}
	headPayload, err := json.MarshalIndent(head, "", "  ")
	if err != nil {
		return fmt.Errorf("encode lane head: %w", err)
	}
	objects := map[string][]byte{
		releaseObjectPrefix + head.ReleaseSHA256 + ".json":   releasePayload,
		policyObjectPrefix + Digest(policyPayload) + ".json": policyPayload,
		directoryHeadFile: append(headPayload, '\n'),
	}
	for relative, payload := range objects {
		path := filepath.Join(dir, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("create local policy directory: %w", err)
		}
		if err := os.WriteFile(path, payload, 0o644); err != nil {
			return fmt.Errorf("write local policy object: %w", err)
		}
	}
	return nil
}
