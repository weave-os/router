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

// PublishServingManifest publishes validated v2 manifest bytes to artifacts/, addressed by their
// own digest, without activating any target. The v1 namespaces are read-only.
func (r *Registry) PublishServingManifest(ctx context.Context, kind ServingKind, payload []byte) (ObjectRef, error) {
	if _, err := DecodePublishableServingManifest(payload, r.rootURI, kind); err != nil {
		return ObjectRef{}, err
	}
	digest := Digest(payload)
	return r.publishImmutable(ctx, r.prefix+"/"+servingArtifactsNamespace+digest+".json", payload, digest)
}

// ReadServingObject validates the reference, namespace and schema, and returns the manifest
// decoded from the exact stored payload at the referenced generation. The layout the reference
// uses selects the decoder: legacy namespaces hold v1 objects, artifacts/ holds v2 objects.
func (r *Registry) ReadServingObject(ctx context.Context, kind ServingKind, ref ObjectRef) (ServingManifest, []byte, error) {
	namespace, err := servingRefNamespace(ref, r.rootURI, kind)
	if err != nil {
		return nil, nil, err
	}
	payload, err := r.readExact(ctx, ref, namespace)
	if err != nil {
		return nil, nil, err
	}
	manifest, err := DecodeServingObject(payload, r.rootURI, kind, ref)
	if err != nil {
		return nil, nil, err
	}
	return manifest, payload, nil
}

// ServingRef resolves a selected digest once, before proposal approval. A v2 kind resolves the
// artifacts/ layout first and falls back to the legacy namespace of the v1 object it folds.
func (r *Registry) ServingRef(ctx context.Context, kind ServingKind, digest string) (ObjectRef, error) {
	if !validDigest(digest) {
		return ObjectRef{}, errors.New("invalid serving digest")
	}
	namespaces, err := servingNamespaces(kind)
	if err != nil {
		return ObjectRef{}, err
	}
	for index, namespace := range namespaces {
		ref, err := r.objectRef(ctx, r.prefix+"/"+namespace+digest+".json", digest)
		if errors.Is(err, ErrNotFound) && index < len(namespaces)-1 {
			continue
		}
		return ref, err
	}
	return ObjectRef{}, ErrNotFound
}

// servingStateName is the authoritative mutable state object for a target.
func (r *Registry) servingStateName(target ServingTarget) (string, error) {
	environment, err := target.Environment()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/state/%s/%s.json", r.prefix, environment, target), nil
}

// servingLegacyStateName is the pre-v2 state object; it is read only until the target's
// first write to servingStateName and is never written afterwards.
func (r *Registry) servingLegacyStateName(target ServingTarget) (string, error) {
	environment, err := target.Environment()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/runtime_state/router_serving/v1/targets/%s/%s/state.json", r.prefix, environment, target), nil
}

// ReadServingState performs an authoritative read on every admission; there is no stale-state
// fallback. The new state path wins whenever it exists; a target that has not yet been written
// under the new layout is served from its legacy state object and flagged as such.
func (r *Registry) ReadServingState(ctx context.Context, target ServingTarget) (ServingStateSnapshot, error) {
	name, err := r.servingStateName(target)
	if err != nil {
		return ServingStateSnapshot{}, err
	}
	snapshot, err := r.readServingStateObject(ctx, name, target)
	if !errors.Is(err, ErrNotFound) {
		return snapshot, err
	}
	legacyName, err := r.servingLegacyStateName(target)
	if err != nil {
		return ServingStateSnapshot{}, err
	}
	snapshot, err = r.readServingStateObject(ctx, legacyName, target)
	if err != nil {
		return ServingStateSnapshot{}, err
	}
	snapshot.LegacyPath = true
	return snapshot, nil
}

func (r *Registry) readServingStateObject(ctx context.Context, name string, target ServingTarget) (ServingStateSnapshot, error) {
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
// Writes always land on state/<environment>/<target>.json: a target still served from the legacy
// runtime_state object is migrated by its first write, which the caller issues with generation 0
// (ServingStateSnapshot.WriteGeneration) so that DoesNotExist guards the bootstrap. The legacy
// object is never rewritten.
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
