package gcsregistry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"weave-os/router/internal/policyregistry"

	"cloud.google.com/go/storage"
)

func (r *Registry) VerifyServingArtifact(ctx context.Context, ref policyregistry.ObjectRef) error {
	if err := policyregistry.ValidateArtifactRef(ref); err != nil {
		return err
	}
	bucket, name, err := policyregistry.ParseGCSObjectURI(ref.URI)
	if err != nil {
		return err
	}
	reader, err := r.client.Bucket(bucket).Object(name).Generation(ref.Generation).NewReader(ctx)
	if err != nil {
		return classifyStorageError("read serving evidence artifact", err)
	}
	defer func() { _ = reader.Close() }()
	payload, err := io.ReadAll(io.LimitReader(reader, policyregistry.MaxPolicyObjectBytes+1))
	if err != nil {
		return fmt.Errorf("read serving evidence artifact: %w", err)
	}
	if len(payload) > policyregistry.MaxPolicyObjectBytes {
		return errors.New("serving evidence artifact exceeds size limit")
	}
	if policyregistry.Digest(payload) != ref.SHA256 {
		return errors.New("serving evidence artifact digest mismatch")
	}
	return nil
}

func (r *Registry) PublishServingManifest(ctx context.Context, kind policyregistry.ServingKind, payload []byte) (policyregistry.ObjectRef, error) {
	if _, err := policyregistry.DecodePublishableServingManifest(payload, r.rootURI, kind); err != nil {
		return policyregistry.ObjectRef{}, err
	}
	digest := policyregistry.Digest(payload)
	return r.publishImmutable(ctx, r.prefix+"/"+policyregistry.ServingArtifactsNamespace+digest+".json", payload, digest)
}

func (r *Registry) ReadServingObject(ctx context.Context, kind policyregistry.ServingKind, ref policyregistry.ObjectRef) (policyregistry.ServingManifest, []byte, error) {
	namespace, err := policyregistry.ServingRefNamespace(ref, r.rootURI, kind)
	if err != nil {
		return nil, nil, err
	}
	payload, err := r.readExact(ctx, ref, namespace)
	if err != nil {
		return nil, nil, err
	}
	manifest, err := policyregistry.DecodeServingObject(payload, r.rootURI, kind, ref)
	if err != nil {
		return nil, nil, err
	}
	return manifest, payload, nil
}

func (r *Registry) ServingRef(ctx context.Context, kind policyregistry.ServingKind, digest string) (policyregistry.ObjectRef, error) {
	if !policyregistry.ValidDigest(digest) {
		return policyregistry.ObjectRef{}, errors.New("invalid serving digest")
	}
	namespaces, err := policyregistry.ServingNamespaces(kind)
	if err != nil {
		return policyregistry.ObjectRef{}, err
	}
	for index, namespace := range namespaces {
		ref, err := r.objectRef(ctx, r.prefix+"/"+namespace+digest+".json", digest)
		if errors.Is(err, policyregistry.ErrNotFound) && index < len(namespaces)-1 {
			continue
		}
		return ref, err
	}
	return policyregistry.ObjectRef{}, policyregistry.ErrNotFound
}

func (r *Registry) servingStateName(target policyregistry.ServingTarget) (string, error) {
	environment, err := target.Environment()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/state/%s/%s.json", r.prefix, environment, target), nil
}

func (r *Registry) servingLegacyStateName(target policyregistry.ServingTarget) (string, error) {
	environment, err := target.Environment()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/runtime_state/router_serving/v1/targets/%s/%s/state.json", r.prefix, environment, target), nil
}

func (r *Registry) ReadServingState(ctx context.Context, target policyregistry.ServingTarget) (policyregistry.ServingStateSnapshot, error) {
	name, err := r.servingStateName(target)
	if err != nil {
		return policyregistry.ServingStateSnapshot{}, err
	}
	snapshot, err := r.readServingStateObject(ctx, name, target)
	if !errors.Is(err, policyregistry.ErrNotFound) {
		return snapshot, err
	}
	legacyName, err := r.servingLegacyStateName(target)
	if err != nil {
		return policyregistry.ServingStateSnapshot{}, err
	}
	snapshot, err = r.readServingStateObject(ctx, legacyName, target)
	if err != nil {
		return policyregistry.ServingStateSnapshot{}, err
	}
	snapshot.LegacyPath = true
	return snapshot, nil
}

const servingStateReadsAcrossReplacement = 3

func (r *Registry) readServingStateObject(ctx context.Context, name string, target policyregistry.ServingTarget) (policyregistry.ServingStateSnapshot, error) {
	var attrs *storage.ObjectAttrs
	var payload []byte
	for attempt := 1; ; attempt++ {
		var err error
		attrs, err = r.bucket.Object(name).Attrs(ctx)
		if err != nil {
			return policyregistry.ServingStateSnapshot{}, classifyStorageError("read serving control-state attributes", err)
		}
		payload, err = r.readObject(ctx, name, attrs.Generation)
		if err == nil {
			break
		}
		if !errors.Is(err, policyregistry.ErrNotFound) {
			return policyregistry.ServingStateSnapshot{}, err
		}
		if attempt == servingStateReadsAcrossReplacement {
			return policyregistry.ServingStateSnapshot{}, fmt.Errorf("serving control state %s kept changing during %d reads; last generation %d", name, attempt, attrs.Generation)
		}
	}
	var state policyregistry.ServingControlState
	if err := policyregistry.StrictDecode(payload, &state); err != nil {
		return policyregistry.ServingStateSnapshot{}, err
	}
	if err := state.Validate(r.rootURI, target); err != nil {
		return policyregistry.ServingStateSnapshot{}, err
	}
	return policyregistry.ServingStateSnapshot{State: state, Generation: attrs.Generation}, nil
}

func (r *Registry) CompareAndSwapServingState(ctx context.Context, next policyregistry.ServingControlState, expectedGeneration int64) (policyregistry.ServingStateSnapshot, error) {
	if expectedGeneration < 0 {
		return policyregistry.ServingStateSnapshot{}, errors.New("negative expected serving generation")
	}
	if err := next.Validate(r.rootURI, next.Target); err != nil {
		return policyregistry.ServingStateSnapshot{}, err
	}
	name, err := r.servingStateName(next.Target)
	if err != nil {
		return policyregistry.ServingStateSnapshot{}, err
	}
	payload, err := json.Marshal(next)
	if err != nil {
		return policyregistry.ServingStateSnapshot{}, err
	}
	condition := storage.Conditions{GenerationMatch: expectedGeneration}
	if expectedGeneration == 0 {
		condition = storage.Conditions{DoesNotExist: true}
	}
	writer := r.bucket.Object(name).If(condition).NewWriter(ctx)
	writer.ContentType = "application/json"
	if _, err := writer.Write(payload); err != nil {
		_ = writer.Close()
		return policyregistry.ServingStateSnapshot{}, classifyStorageError("write serving control state", err)
	}
	if err := writer.Close(); err != nil {
		return policyregistry.ServingStateSnapshot{}, classifyStorageError("commit serving control state; retry the same proposal to resolve ambiguous outcomes", err)
	}
	attrs := writer.Attrs()
	if attrs == nil || attrs.Generation <= expectedGeneration {
		return policyregistry.ServingStateSnapshot{}, errors.New("serving activation committed without a newer generation; retry the same proposal to resolve outcome")
	}
	return policyregistry.ServingStateSnapshot{State: next, Generation: attrs.Generation}, nil
}
