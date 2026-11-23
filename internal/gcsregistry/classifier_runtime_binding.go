package gcsregistry

import (
	"context"
	"errors"
	"fmt"

	"weave-os/router/internal/policyregistry"
)

func (r *Registry) ReadClassifierRuntimeBinding(ctx context.Context, environment policyregistry.Environment, releaseSHA256 string) (policyregistry.ResolvedClassifierRuntimeBinding, error) {
	if !policyregistry.ValidDigest(releaseSHA256) {
		return policyregistry.ResolvedClassifierRuntimeBinding{}, errors.New("classifier runtime binding requires a release digest")
	}
	if _, supported := policyregistry.ClassifierWorkerProjects[environment]; !supported {
		return policyregistry.ResolvedClassifierRuntimeBinding{}, fmt.Errorf("unsupported classifier runtime environment %q", environment)
	}
	name := r.prefix + "/" + policyregistry.ClassifierRuntimeBindingRelativeName(environment, releaseSHA256)
	attrs, err := r.bucket.Object(name).Attrs(ctx)
	if err != nil {
		return policyregistry.ResolvedClassifierRuntimeBinding{}, classifyStorageError("read classifier runtime binding attributes", err)
	}
	payload, err := r.readObject(ctx, name, attrs.Generation)
	if err != nil {
		return policyregistry.ResolvedClassifierRuntimeBinding{}, err
	}
	binding, err := policyregistry.DecodeClassifierRuntimeBinding(payload, environment, releaseSHA256)
	if err != nil {
		return policyregistry.ResolvedClassifierRuntimeBinding{}, err
	}
	return policyregistry.ResolvedClassifierRuntimeBinding{
		Ref:     policyregistry.ObjectRef{URI: policyregistry.ClassifierRuntimeBindingURI(r.rootURI, environment, releaseSHA256), SHA256: policyregistry.Digest(payload), Generation: attrs.Generation},
		Binding: binding,
	}, nil
}
