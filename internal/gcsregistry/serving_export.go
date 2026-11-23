package gcsregistry

import (
	"context"

	"weave-os/router/internal/policyregistry"
)

func (r *Registry) ReadPolicyPayload(ctx context.Context, ref policyregistry.ObjectRef) ([]byte, error) {
	return r.readExact(ctx, ref, policyregistry.PolicyObjectPrefix)
}
