package policyregistry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"cloud.google.com/go/storage"
	"google.golang.org/api/googleapi"

	"weave-os/router/internal/router/hmm/rosterdata"
	"weave-os/router/internal/router/hmm/selectionpolicy"
)

const maxPolicyObjectBytes = 8 << 20

var (
	ErrNotFound = errors.New("router policy registry object not found")
	ErrConflict = errors.New("router policy lane-head generation conflict")
)

type Registry struct {
	client     *storage.Client
	bucket     *storage.BucketHandle
	bucketName string
	prefix     string
	rootURI    string
}

func NewRegistry(client *storage.Client, registryURI string) (*Registry, error) {
	if client == nil {
		return nil, errors.New("router policy registry requires a storage client")
	}
	bucketName, prefix, err := parseRegistryURI(registryURI)
	if err != nil {
		return nil, err
	}
	return &Registry{
		client:     client,
		bucket:     client.Bucket(bucketName),
		bucketName: bucketName,
		prefix:     prefix,
		rootURI:    "gs://" + bucketName + "/" + prefix,
	}, nil
}

func NewGCSRegistry(ctx context.Context, registryURI string) (*Registry, error) {
	client, err := storage.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("create router policy storage client: %w", err)
	}
	registry, err := NewRegistry(client, registryURI)
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	return registry, nil
}

func (r *Registry) Close() error { return r.client.Close() }

func (r *Registry) RootURI() string { return r.rootURI }

func (r *Registry) PublishPolicy(ctx context.Context, admitted *selectionpolicy.Policy) (ObjectRef, error) {
	canonical, err := rosterdata.CanonicalBytes(admitted.Roster())
	if err != nil {
		return ObjectRef{}, err
	}
	digest := Digest(canonical)
	return r.publishImmutable(ctx, r.policyObjectName(digest), canonical, digest)
}

func (r *Registry) PublishRelease(ctx context.Context, release Release) (ObjectRef, error) {
	if err := release.Validate(r.rootURI); err != nil {
		return ObjectRef{}, err
	}
	canonical, err := CanonicalBytes(release)
	if err != nil {
		return ObjectRef{}, err
	}
	digest := Digest(canonical)
	return r.publishImmutable(ctx, r.releaseObjectName(digest), canonical, digest)
}

func (r *Registry) ReadRelease(ctx context.Context, ref ObjectRef) (Release, error) {
	payload, err := r.readExact(ctx, ref, "router_policy/v1/releases/sha256/")
	if err != nil {
		return Release{}, err
	}
	release, err := DecodeRelease(payload, r.rootURI)
	if err != nil {
		return Release{}, err
	}
	return release, nil
}

func (r *Registry) ExportHead(ctx context.Context, environment Environment, lane Lane, dir, classifierImageDigest string) (HeadSnapshot, error) {
	head, err := r.ReadHead(ctx, environment, lane)
	if err != nil {
		return HeadSnapshot{}, err
	}
	releasePayload, err := r.readExact(ctx, ObjectRef{
		URI: head.Head.ReleaseURI, SHA256: head.Head.ReleaseSHA256, Generation: head.Head.ReleaseGeneration,
	}, releaseObjectPrefix)
	if err != nil {
		return HeadSnapshot{}, err
	}
	release, err := DecodeRelease(releasePayload, r.rootURI)
	if err != nil {
		return HeadSnapshot{}, err
	}
	policyRef := ObjectRef{URI: release.Policy.URI, SHA256: release.Policy.SHA256, Generation: release.Policy.Generation}
	if _, err := r.ReadPolicy(ctx, policyRef); err != nil {
		return HeadSnapshot{}, err
	}
	policyPayload, err := r.readExact(ctx, policyRef, policyObjectPrefix)
	if err != nil {
		return HeadSnapshot{}, err
	}
	if classifierImageDigest != "" && classifierImageDigest != release.Classifier.ImageDigest {
		release.Classifier.ImageDigest = classifierImageDigest
		if err := release.Validate(r.rootURI); err != nil {
			return HeadSnapshot{}, err
		}
		if releasePayload, err = json.Marshal(release); err != nil {
			return HeadSnapshot{}, fmt.Errorf("encode re-pinned release: %w", err)
		}
		head.Head.ReleaseSHA256 = Digest(releasePayload)
		head.Head.ReleaseURI = r.rootURI + "/" + releaseObjectPrefix + head.Head.ReleaseSHA256 + ".json"
	}
	return head, WriteDirectoryRelease(dir, head.Head, releasePayload, policyPayload)
}

func (r *Registry) ReadPolicy(ctx context.Context, ref ObjectRef) (*selectionpolicy.Policy, error) {
	payload, err := r.readExact(ctx, ref, policyObjectPrefix)
	if err != nil {
		return nil, err
	}
	return selectionpolicy.Admit(payload)
}

func (r *Registry) ReleaseRef(ctx context.Context, digest string) (ObjectRef, error) {
	if !validDigest(digest) {
		return ObjectRef{}, fmt.Errorf("invalid release digest %q", digest)
	}
	return r.objectRef(ctx, r.releaseObjectName(digest), digest)
}

func (r *Registry) ReadHead(ctx context.Context, environment Environment, lane Lane) (HeadSnapshot, error) {
	if err := ValidateEnvironment(environment); err != nil {
		return HeadSnapshot{}, err
	}
	if err := ValidateLane(lane); err != nil {
		return HeadSnapshot{}, err
	}
	name := r.headObjectName(environment, lane)
	attrs, err := r.bucket.Object(name).Attrs(ctx)
	if err != nil {
		return HeadSnapshot{}, classifyStorageError("read lane-head attributes", err)
	}
	payload, err := r.readObject(ctx, name, attrs.Generation)
	if err != nil {
		return HeadSnapshot{}, err
	}
	head, err := DecodeLaneHead(payload, r.rootURI, environment, lane)
	if err != nil {
		return HeadSnapshot{}, err
	}
	return HeadSnapshot{Head: head, Generation: attrs.Generation}, nil
}

func (r *Registry) Promote(ctx context.Context, head LaneHead, expectedGeneration int64) (HeadSnapshot, error) {
	if expectedGeneration < 0 {
		return HeadSnapshot{}, errors.New("expected lane-head generation cannot be negative")
	}
	if err := head.Validate(r.rootURI, head.Environment, head.Lane); err != nil {
		return HeadSnapshot{}, err
	}
	releaseRef := ObjectRef{URI: head.ReleaseURI, SHA256: head.ReleaseSHA256, Generation: head.ReleaseGeneration}
	release, err := r.ReadRelease(ctx, releaseRef)
	if err != nil {
		return HeadSnapshot{}, fmt.Errorf("validate promoted release: %w", err)
	}
	policyRef := ObjectRef{URI: release.Policy.URI, SHA256: release.Policy.SHA256, Generation: release.Policy.Generation}
	selectionPolicy, err := r.ReadPolicy(ctx, policyRef)
	if err != nil {
		return HeadSnapshot{}, fmt.Errorf("validate promoted selection policy: %w", err)
	}
	if !slices.Equal(selectionPolicy.Roster().ClassOrder, release.Classifier.ClassOrder) {
		return HeadSnapshot{}, errors.New("promoted classifier class order does not match selection policy")
	}
	payload, err := CanonicalBytes(head)
	if err != nil {
		return HeadSnapshot{}, err
	}
	object := r.bucket.Object(r.headObjectName(head.Environment, head.Lane))
	if expectedGeneration == 0 {
		object = object.If(storage.Conditions{DoesNotExist: true})
	} else {
		object = object.If(storage.Conditions{GenerationMatch: expectedGeneration})
	}
	writer := object.NewWriter(ctx)
	writer.ContentType = "application/json"
	if _, err := writer.Write(payload); err != nil {
		_ = writer.Close()
		return HeadSnapshot{}, classifyStorageError("write lane head", err)
	}
	if err := writer.Close(); err != nil {
		return HeadSnapshot{}, classifyStorageError("commit lane head", err)
	}
	attrs := writer.Attrs()
	if attrs == nil || attrs.Generation <= expectedGeneration {
		return HeadSnapshot{}, errors.New("lane-head write returned no newer generation")
	}
	stored, err := r.readObject(ctx, r.headObjectName(head.Environment, head.Lane), attrs.Generation)
	if err != nil {
		return HeadSnapshot{}, err
	}
	if !bytes.Equal(stored, payload) {
		return HeadSnapshot{}, errors.New("lane-head read-after-write bytes differ")
	}
	return HeadSnapshot{Head: head, Generation: attrs.Generation}, nil
}

func (r *Registry) publishImmutable(ctx context.Context, name string, payload []byte, digest string) (ObjectRef, error) {
	object := r.bucket.Object(name).If(storage.Conditions{DoesNotExist: true})
	writer := object.NewWriter(ctx)
	writer.ContentType = "application/json"
	if _, err := writer.Write(payload); err != nil {
		_ = writer.Close()
		return ObjectRef{}, fmt.Errorf("write immutable router policy object: %w", err)
	}
	closeErr := writer.Close()
	if closeErr != nil {
		if !isPreconditionFailure(closeErr) {
			return ObjectRef{}, fmt.Errorf("commit immutable router policy object: %w", closeErr)
		}
		return r.verifyExistingImmutable(ctx, name, payload, digest)
	}
	attrs := writer.Attrs()
	if attrs == nil || attrs.Generation <= 0 {
		return ObjectRef{}, errors.New("immutable policy write returned no generation")
	}
	ref := ObjectRef{URI: r.objectURI(name), SHA256: digest, Generation: attrs.Generation}
	stored, err := r.readObject(ctx, name, ref.Generation)
	if err != nil {
		return ObjectRef{}, err
	}
	if !bytes.Equal(stored, payload) {
		return ObjectRef{}, errors.New("immutable policy read-after-write bytes differ")
	}
	return ref, nil
}

func (r *Registry) verifyExistingImmutable(ctx context.Context, name string, payload []byte, digest string) (ObjectRef, error) {
	ref, err := r.objectRef(ctx, name, digest)
	if err != nil {
		return ObjectRef{}, err
	}
	stored, err := r.readObject(ctx, name, ref.Generation)
	if err != nil {
		return ObjectRef{}, err
	}
	if !bytes.Equal(stored, payload) {
		return ObjectRef{}, errors.New("content-addressed object exists with different bytes")
	}
	return ref, nil
}

func (r *Registry) objectRef(ctx context.Context, name, digest string) (ObjectRef, error) {
	attrs, err := r.bucket.Object(name).Attrs(ctx)
	if err != nil {
		return ObjectRef{}, classifyStorageError("read immutable object attributes", err)
	}
	return ObjectRef{URI: r.objectURI(name), SHA256: digest, Generation: attrs.Generation}, nil
}

func (r *Registry) readExact(ctx context.Context, ref ObjectRef, requiredPathPrefix string) ([]byte, error) {
	if !validDigest(ref.SHA256) || ref.Generation <= 0 {
		return nil, errors.New("immutable object reference has invalid digest or generation")
	}
	name, err := r.objectName(ref.URI)
	if err != nil {
		return nil, err
	}
	relative := strings.TrimPrefix(name, r.prefix+"/")
	if !strings.HasPrefix(relative, requiredPathPrefix) || !strings.HasSuffix(relative, "/"+ref.SHA256+".json") {
		return nil, errors.New("immutable object URI does not match its digest path")
	}
	return r.readObject(ctx, name, ref.Generation)
}

func (r *Registry) readObject(ctx context.Context, name string, generation int64) ([]byte, error) {
	reader, err := r.bucket.Object(name).Generation(generation).NewReader(ctx)
	if err != nil {
		return nil, classifyStorageError("open router policy object", err)
	}
	defer func() { _ = reader.Close() }()
	payload, err := io.ReadAll(io.LimitReader(reader, maxPolicyObjectBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read router policy object: %w", err)
	}
	if len(payload) > maxPolicyObjectBytes {
		return nil, fmt.Errorf("router policy object exceeds %d bytes", maxPolicyObjectBytes)
	}
	return payload, nil
}

func (r *Registry) policyObjectName(digest string) string {
	return r.prefix + "/router_policy/v1/policies/sha256/" + digest + ".json"
}

func (r *Registry) releaseObjectName(digest string) string {
	return r.prefix + "/router_policy/v1/releases/sha256/" + digest + ".json"
}

func (r *Registry) headObjectName(environment Environment, lane Lane) string {
	return fmt.Sprintf("%s/runtime_state/router_policy/v1/environments/%s/lanes/%s/head.json", r.prefix, environment, lane)
}

func (r *Registry) objectURI(name string) string { return "gs://" + r.bucketName + "/" + name }

func (r *Registry) objectName(uri string) (string, error) {
	bucketName, name, err := parseGCSObjectURI(uri)
	if err != nil {
		return "", err
	}
	if bucketName != r.bucketName || !strings.HasPrefix(name, r.prefix+"/") {
		return "", errors.New("router policy object is outside configured registry")
	}
	return name, nil
}

func parseRegistryURI(uri string) (string, string, error) {
	bucketName, prefix, err := parseGCSObjectURI(strings.TrimRight(strings.TrimSpace(uri), "/"))
	if err != nil {
		return "", "", err
	}
	if prefix == "" {
		return "", "", errors.New("router policy registry URI requires an object prefix")
	}
	return bucketName, prefix, nil
}

func parseGCSObjectURI(uri string) (string, string, error) {
	if !strings.HasPrefix(uri, "gs://") {
		return "", "", fmt.Errorf("invalid GCS URI %q", uri)
	}
	remainder := strings.TrimPrefix(uri, "gs://")
	bucketName, name, found := strings.Cut(remainder, "/")
	if bucketName == "" || !found || strings.Trim(name, "/") == "" {
		return "", "", fmt.Errorf("invalid GCS object URI %q", uri)
	}
	if strings.Contains(name, "//") || strings.Contains(name, "..") {
		return "", "", fmt.Errorf("invalid GCS object path %q", name)
	}
	return bucketName, strings.Trim(name, "/"), nil
}

func classifyStorageError(action string, err error) error {
	if errors.Is(err, storage.ErrObjectNotExist) {
		return fmt.Errorf("%s: %w", action, ErrNotFound)
	}
	if isPreconditionFailure(err) {
		return fmt.Errorf("%s: %w", action, ErrConflict)
	}
	return fmt.Errorf("%s: %w", action, err)
}

func isPreconditionFailure(err error) bool {
	var apiErr *googleapi.Error
	return errors.As(err, &apiErr) && apiErr.Code == 412
}
