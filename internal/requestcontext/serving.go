package requestcontext

import (
	"context"
	"crypto/sha256"
	"encoding/json"
)

type ServingIdentity struct {
	Target             string `json:"target"`
	ActivationID       string `json:"activation_id"`
	ReleaseID          string `json:"release_id"`
	BindingID          string `json:"binding_id"`
	ProfileKey         string `json:"profile_key,omitempty"`
	ProfileName        string `json:"profile_name,omitempty"`
	ProfileRevision    string `json:"profile_revision,omitempty"`
	Plan               string `json:"plan,omitempty"`
	EntitlementVersion int64  `json:"entitlement_version,omitempty"`
	BindingGeneration  int64  `json:"binding_generation"`
	StateNamespace     string `json:"-"`
	CredentialIdentity string `json:"-"`
}

type servingIdentityContextKey struct{}

func WithServingIdentity(ctx context.Context, identity ServingIdentity) context.Context {
	return context.WithValue(ctx, servingIdentityContextKey{}, identity)
}

func ServingIdentityFromContext(ctx context.Context) (ServingIdentity, bool) {
	identity, ok := ctx.Value(servingIdentityContextKey{}).(ServingIdentity)
	return identity, ok
}

func ServingStateKey(ctx context.Context, key []byte) []byte {
	identity, managed := ServingIdentityFromContext(ctx)
	if !managed {
		return key
	}
	payload, _ := json.Marshal(struct {
		Namespace string
		Key       []byte
	}{identity.StateNamespace, key})
	digest := sha256.Sum256(payload)
	return digest[:]
}
