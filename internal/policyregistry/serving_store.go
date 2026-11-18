package policyregistry

import (
	"context"

	"weave-os/router/internal/router/hmm/selectionpolicy"
)

type ServingStore interface {
	RootURI() string
	ReadServingObject(context.Context, ServingKind, ObjectRef) (ServingManifest, []byte, error)
	ReadPolicy(context.Context, ObjectRef) (*selectionpolicy.Policy, error)
	ReadServingState(context.Context, ServingTarget) (ServingStateSnapshot, error)
	CompareAndSwapServingState(context.Context, ServingControlState, int64) (ServingStateSnapshot, error)
	ReadClassifierRuntimeBinding(context.Context, Environment, string) (ResolvedClassifierRuntimeBinding, error)
}

type ServingValidationStore interface {
	ServingStore
	VerifyServingArtifact(context.Context, ObjectRef) error
}
