package gcsregistry

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"cloud.google.com/go/storage"
	"google.golang.org/api/googleapi"

	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/router/rosterdata"
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

func (r *Registry) ReadRelease(ctx context.Context, ref policyregistry.ObjectRef) (policyregistry.Release, error) {
	payload, err := r.readExact(ctx, ref, "router_policy/v1/releases/sha256/")
	if err != nil {
		return policyregistry.Release{}, err
	}
	release, err := policyregistry.DecodeRelease(payload, r.rootURI)
	if err != nil {
		return policyregistry.Release{}, err
	}
	return release, nil
}

func (r *Registry) ReadPolicy(ctx context.Context, ref policyregistry.ObjectRef) (*rosterdata.Roster, error) {
	roster, err := r.ReadServingPolicy(ctx, ref)
	if err != nil {
		return nil, err
	}
	if err := rosterdata.ValidateCatalog(roster); err != nil {
		return nil, err
	}
	return roster, nil
}

func (r *Registry) ReadServingPolicy(ctx context.Context, ref policyregistry.ObjectRef) (*rosterdata.Roster, error) {
	payload, err := r.readExact(ctx, ref, "router_policy/v1/policies/sha256/")
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

func (r *Registry) ReadHead(ctx context.Context, environment policyregistry.Environment, lane policyregistry.Lane) (policyregistry.HeadSnapshot, error) {
	if err := policyregistry.ValidateEnvironment(environment); err != nil {
		return policyregistry.HeadSnapshot{}, err
	}
	if err := policyregistry.ValidateLane(lane); err != nil {
		return policyregistry.HeadSnapshot{}, err
	}
	name := r.headObjectName(environment, lane)
	attrs, err := r.bucket.Object(name).Attrs(ctx)
	if err != nil {
		return policyregistry.HeadSnapshot{}, classifyStorageError("read lane-head attributes", err)
	}
	payload, err := r.readObject(ctx, name, attrs.Generation)
	if err != nil {
		return policyregistry.HeadSnapshot{}, err
	}
	head, err := policyregistry.DecodeLaneHead(payload, r.rootURI, environment, lane)
	if err != nil {
		return policyregistry.HeadSnapshot{}, err
	}
	return policyregistry.HeadSnapshot{Head: head, Generation: attrs.Generation}, nil
}

func (r *Registry) publishImmutable(ctx context.Context, name string, payload []byte, digest string) (policyregistry.ObjectRef, error) {
	object := r.bucket.Object(name).If(storage.Conditions{DoesNotExist: true})
	writer := object.NewWriter(ctx)
	writer.ContentType = "application/json"
	if _, err := writer.Write(payload); err != nil {
		_ = writer.Close()
		return policyregistry.ObjectRef{}, fmt.Errorf("write immutable router policy object: %w", err)
	}
	closeErr := writer.Close()
	if closeErr != nil {
		if !isPreconditionFailure(closeErr) {
			return policyregistry.ObjectRef{}, fmt.Errorf("commit immutable router policy object: %w", closeErr)
		}
		return r.verifyExistingImmutable(ctx, name, payload, digest)
	}
	attrs := writer.Attrs()
	if attrs == nil || attrs.Generation <= 0 {
		return policyregistry.ObjectRef{}, errors.New("immutable policy write returned no generation")
	}
	ref := policyregistry.ObjectRef{URI: r.objectURI(name), SHA256: digest, Generation: attrs.Generation}
	stored, err := r.readObject(ctx, name, ref.Generation)
	if err != nil {
		return policyregistry.ObjectRef{}, err
	}
	if !bytes.Equal(stored, payload) {
		return policyregistry.ObjectRef{}, errors.New("immutable policy read-after-write bytes differ")
	}
	return ref, nil
}

func (r *Registry) verifyExistingImmutable(ctx context.Context, name string, payload []byte, digest string) (policyregistry.ObjectRef, error) {
	ref, err := r.objectRef(ctx, name, digest)
	if err != nil {
		return policyregistry.ObjectRef{}, err
	}
	stored, err := r.readObject(ctx, name, ref.Generation)
	if err != nil {
		return policyregistry.ObjectRef{}, err
	}
	if !bytes.Equal(stored, payload) {
		return policyregistry.ObjectRef{}, errors.New("content-addressed object exists with different bytes")
	}
	return ref, nil
}

func (r *Registry) objectRef(ctx context.Context, name, digest string) (policyregistry.ObjectRef, error) {
	attrs, err := r.bucket.Object(name).Attrs(ctx)
	if err != nil {
		return policyregistry.ObjectRef{}, classifyStorageError("read immutable object attributes", err)
	}
	return policyregistry.ObjectRef{URI: r.objectURI(name), SHA256: digest, Generation: attrs.Generation}, nil
}

func (r *Registry) readExact(ctx context.Context, ref policyregistry.ObjectRef, requiredPathPrefix string) ([]byte, error) {
	if !policyregistry.ValidDigest(ref.SHA256) || ref.Generation <= 0 {
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
	payload, err := io.ReadAll(io.LimitReader(reader, policyregistry.MaxPolicyObjectBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read router policy object: %w", err)
	}
	if len(payload) > policyregistry.MaxPolicyObjectBytes {
		return nil, fmt.Errorf("router policy object exceeds %d bytes", policyregistry.MaxPolicyObjectBytes)
	}
	return payload, nil
}

func (r *Registry) headObjectName(environment policyregistry.Environment, lane policyregistry.Lane) string {
	return fmt.Sprintf("%s/runtime_state/router_policy/v1/environments/%s/lanes/%s/head.json", r.prefix, environment, lane)
}

func (r *Registry) objectURI(name string) string { return "gs://" + r.bucketName + "/" + name }

func (r *Registry) objectName(uri string) (string, error) {
	bucketName, name, err := policyregistry.ParseGCSObjectURI(uri)
	if err != nil {
		return "", err
	}
	if bucketName != r.bucketName || !strings.HasPrefix(name, r.prefix+"/") {
		return "", errors.New("router policy object is outside configured registry")
	}
	return name, nil
}

func parseRegistryURI(uri string) (string, string, error) {
	bucketName, prefix, err := policyregistry.ParseGCSObjectURI(strings.TrimRight(strings.TrimSpace(uri), "/"))
	if err != nil {
		return "", "", err
	}
	if prefix == "" {
		return "", "", errors.New("router policy registry URI requires an object prefix")
	}
	return bucketName, prefix, nil
}

func classifyStorageError(action string, err error) error {
	if errors.Is(err, storage.ErrObjectNotExist) {
		return fmt.Errorf("%s: %w", action, policyregistry.ErrNotFound)
	}
	if isPreconditionFailure(err) {
		return fmt.Errorf("%s: %w", action, policyregistry.ErrConflict)
	}
	return fmt.Errorf("%s: %w", action, err)
}

func isPreconditionFailure(err error) bool {
	var apiErr *googleapi.Error
	return errors.As(err, &apiErr) && apiErr.Code == 412
}
