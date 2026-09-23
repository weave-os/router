package policyregistry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"cloud.google.com/go/storage"
)

// VerifyServingArtifact checks audit/provenance bytes, including their exact GCS generation.
// It does not interpret evidence semantics or substitute metadata for private revision validation.
func (r *Registry) VerifyServingArtifact(ctx context.Context, ref ObjectRef) error {
	if err := validateArtifactRef(ref); err != nil {
		return err
	}
	bucket, name, err := parseGCSObjectURI(ref.URI)
	if err != nil {
		return err
	}
	reader, err := r.client.Bucket(bucket).Object(name).Generation(ref.Generation).NewReader(ctx)
	if err != nil {
		return classifyStorageError("read serving evidence artifact", err)
	}
	defer reader.Close()
	payload, err := io.ReadAll(io.LimitReader(reader, maxPolicyObjectBytes+1))
	if err != nil {
		return fmt.Errorf("read serving evidence artifact: %w", err)
	}
	if len(payload) > maxPolicyObjectBytes {
		return errors.New("serving evidence artifact exceeds size limit")
	}
	if Digest(payload) != ref.SHA256 {
		return errors.New("serving evidence artifact digest mismatch")
	}
	return nil
}

// PublishServingManifest publishes validated manifest bytes, addressed by their own digest,
// without activating any target.
func (r *Registry) PublishServingManifest(ctx context.Context, kind ServingKind, payload []byte) (ObjectRef, error) {
	if _, err := DecodeServingManifest(payload, r.rootURI, kind); err != nil {
		return ObjectRef{}, err
	}
	namespace, err := servingNamespace(kind)
	if err != nil {
		return ObjectRef{}, err
	}
	digest := Digest(payload)
	return r.publishImmutable(ctx, r.prefix+"/"+namespace+digest+".json", payload, digest)
}

// ReadServingObject validates the reference, namespace and schema, and returns the manifest
// decoded from the exact stored payload at the referenced generation.
func (r *Registry) ReadServingObject(ctx context.Context, kind ServingKind, ref ObjectRef) (ServingManifest, []byte, error) {
	if err := ValidateServingRef(ref, r.rootURI, kind); err != nil {
		return nil, nil, err
	}
	namespace, err := servingNamespace(kind)
	if err != nil {
		return nil, nil, err
	}
	payload, err := r.readExact(ctx, ref, namespace)
	if err != nil {
		return nil, nil, err
	}
	manifest, err := DecodeServingManifest(payload, r.rootURI, kind)
	if err != nil {
		return nil, nil, err
	}
	return manifest, payload, nil
}

// ServingRef resolves a selected digest once, before proposal approval.
func (r *Registry) ServingRef(ctx context.Context, kind ServingKind, digest string) (ObjectRef, error) {
	if !validDigest(digest) {
		return ObjectRef{}, errors.New("invalid serving digest")
	}
	namespace, err := servingNamespace(kind)
	if err != nil {
		return ObjectRef{}, err
	}
	return r.objectRef(ctx, r.prefix+"/"+namespace+digest+".json", digest)
}

func (r *Registry) servingStateName(target ServingTarget) (string, error) {
	environment, err := target.Environment()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/runtime_state/router_serving/v1/targets/%s/%s/state.json", r.prefix, environment, target), nil
}

// ReadServingState performs an authoritative read on every admission; there is no stale-state fallback.
func (r *Registry) ReadServingState(ctx context.Context, target ServingTarget) (ServingStateSnapshot, error) {
	name, err := r.servingStateName(target)
	if err != nil {
		return ServingStateSnapshot{}, err
	}
	attrs, err := r.bucket.Object(name).Attrs(ctx)
	if err != nil {
		return ServingStateSnapshot{}, classifyStorageError("read serving control-state attributes", err)
	}
	payload, err := r.readObject(ctx, name, attrs.Generation)
	if err != nil {
		return ServingStateSnapshot{}, err
	}
	var state ServingControlState
	if err := strictDecode(payload, &state); err != nil {
		return ServingStateSnapshot{}, err
	}
	if err := state.Validate(r.rootURI, target); err != nil {
		return ServingStateSnapshot{}, err
	}
	return ServingStateSnapshot{State: state, Generation: attrs.Generation}, nil
}

// CompareAndSwapServingState commits current activation, supersession and withdrawals in one write.
// This persistence primitive is used only by ServingController, after complete proposal validation.
func (r *Registry) CompareAndSwapServingState(ctx context.Context, next ServingControlState, expectedGeneration int64) (ServingStateSnapshot, error) {
	if expectedGeneration < 0 {
		return ServingStateSnapshot{}, errors.New("negative expected serving generation")
	}
	if err := next.Validate(r.rootURI, next.Target); err != nil {
		return ServingStateSnapshot{}, err
	}
	name, err := r.servingStateName(next.Target)
	if err != nil {
		return ServingStateSnapshot{}, err
	}
	payload, err := json.Marshal(next)
	if err != nil {
		return ServingStateSnapshot{}, err
	}
	condition := storage.Conditions{GenerationMatch: expectedGeneration}
	if expectedGeneration == 0 {
		condition = storage.Conditions{DoesNotExist: true}
	}
	writer := r.bucket.Object(name).If(condition).NewWriter(ctx)
	writer.ContentType = "application/json"
	if _, err := writer.Write(payload); err != nil {
		_ = writer.Close()
		return ServingStateSnapshot{}, classifyStorageError("write serving control state", err)
	}
	if err := writer.Close(); err != nil {
		return ServingStateSnapshot{}, classifyStorageError("commit serving control state; retry the same proposal to resolve ambiguous outcomes", err)
	}
	attrs := writer.Attrs()
	if attrs == nil || attrs.Generation <= expectedGeneration {
		return ServingStateSnapshot{}, errors.New("serving activation committed without a newer generation; retry the same proposal to resolve outcome")
	}
	// Do not turn a successful CAS into a deployment failure because a subsequent read fails.
	return ServingStateSnapshot{State: next, Generation: attrs.Generation}, nil
}
