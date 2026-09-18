package requestcontext

import (
	"context"
	"crypto/sha256"
	"encoding/json"
)

// ServingIdentity is verified admission metadata, never populated from client headers.
// It is independent of registry I/O so routing, billing and telemetry share one value.
type ServingIdentity struct {
	Target             string `json:"target"`
	ActivationID       string `json:"activation_id"`
	ReleaseID          string `json:"release_id"`
	BindingID          string `json:"binding_id"`
	ProfileKey         string `json:"profile_key,omitempty"`
	ProfileRevision    string `json:"profile_revision,omitempty"`
	BindingGeneration  int64  `json:"binding_generation"`
	StateNamespace     string `json:"-"`
	CredentialIdentity string `json:"-"`
}

type servingIdentityContextKey struct{}

// WithServingIdentity carries the admitted tuple through detached outcome contexts too.
func WithServingIdentity(ctx context.Context, identity ServingIdentity) context.Context {
	return context.WithValue(ctx, servingIdentityContextKey{}, identity)
}

// ServingIdentityFromContext distinguishes legacy requests from managed admissions.
func ServingIdentityFromContext(ctx context.Context) (ServingIdentity, bool) {
	identity, ok := ctx.Value(servingIdentityContextKey{}).(ServingIdentity)
	return identity, ok
}

// ServingStateKey isolates learned state across releases and binding incarnations.
// Late writes retain their old namespace and cannot mutate a rebound conversation.
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
