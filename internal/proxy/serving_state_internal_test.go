package proxy

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"
)

func TestEscalationActivationPreservesLegacyIdentityAndIsolatesManagedState(t *testing.T) {
	installationID := uuid.New()
	const material = "credential/strategy/active/1"
	mac := hmac.New(sha256.New, installationID[:])
	_, _ = mac.Write([]byte(material))
	legacy := escalationActivationID(context.Background(), installationID, material)
	require.Equal(t, mac.Sum(nil), legacy[:], "legacy continuation lookup must keep its existing identity")
	old := requestcontext.WithServingIdentity(context.Background(), requestcontext.ServingIdentity{StateNamespace: "release-a-generation-1"})
	rebound := requestcontext.WithServingIdentity(context.Background(), requestcontext.ServingIdentity{StateNamespace: "release-a-generation-2"})
	oldActivation := escalationActivationID(old, installationID, material)
	require.NotEqual(t, legacy, oldActivation)
	require.NotEqual(t, oldActivation, escalationActivationID(rebound, installationID, material))
	require.Equal(t, oldActivation, escalationActivationID(old, installationID, material))
}

func TestManagedCredentialRotationPreservesStateWithoutCrossInstallationLeakage(t *testing.T) {
	env, err := translate.ParseOpenAI([]byte(`{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"task"}]}`))
	require.NoError(t, err)
	base := context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{SessionID: "conversation"})
	assertion := policyregistry.ServingAssertion{
		APIKeyID:  "old-key",
		Scope:     policyregistry.AdmissionScope{InstallationID: "installation-a", CredentialIdentity: "subject", Persistent: true},
		Admission: policyregistry.SessionReleaseBinding{ActivationID: "activation", BindingGeneration: 1},
	}
	old := policyregistry.WithServingAssertion(base, assertion)
	oldKey := deriveSessionKeyForRequest(old, env, "old-key")
	oldForce := deriveForceModelSessionKeyForRequest(old, env, "old-key", oldKey)
	assertion.APIKeyID = "rotated-key"
	rotated := policyregistry.WithServingAssertion(base, assertion)
	rotatedKey := deriveSessionKeyForRequest(rotated, env, "rotated-key")
	require.Equal(t, oldKey, rotatedKey)
	require.Equal(t, oldForce, deriveForceModelSessionKeyForRequest(rotated, env, "rotated-key", rotatedKey))
	assertion.Scope.InstallationID = "installation-b"
	other := policyregistry.WithServingAssertion(base, assertion)
	otherKey := deriveSessionKeyForRequest(other, env, "rotated-key")
	require.NotEqual(t, oldKey, otherKey)
	require.NotEqual(t, oldForce, deriveForceModelSessionKeyForRequest(other, env, "rotated-key", otherKey))
	assertion.Scope.CredentialIdentity = assertion.APIKeyID
	shared := policyregistry.WithServingAssertion(base, assertion)
	sharedKey := deriveSessionKeyForRequest(shared, env, assertion.APIKeyID)
	require.Equal(t, deriveForceModelSessionKeyForRequest(base, env, assertion.APIKeyID, sharedKey), deriveForceModelSessionKeyForRequest(shared, env, assertion.APIKeyID, sharedKey), "shared-key force intent preserves legacy bytes")
}

func TestManagedRebindIsolatesLearnedStateAndPreservesForceIntent(t *testing.T) {
	env, err := translate.ParseOpenAI([]byte(`{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"task"}]}`))
	require.NoError(t, err)
	base := context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{SessionID: "conversation"})
	old := requestcontext.WithServingIdentity(base, requestcontext.ServingIdentity{StateNamespace: "release-a-generation-1"})
	rebound := requestcontext.WithServingIdentity(base, requestcontext.ServingIdentity{StateNamespace: "release-a-generation-2"})
	oldKey := deriveSessionKeyForRequest(old, env, "key")
	newKey := deriveSessionKeyForRequest(rebound, env, "key")
	require.NotEqual(t, oldKey, newKey, "reactivating identical bytes must retire learned state")
	require.Equal(t, oldKey, deriveSessionKeyForRequest(old, env, "key"), "old stream stays on its own namespace")
	oldForce := deriveForceModelSessionKeyForRequest(old, env, "key", oldKey)
	newForce := deriveForceModelSessionKeyForRequest(rebound, env, "key", newKey)
	require.Equal(t, oldForce, newForce, "explicit force intent belongs to the conversation, not its release")
	_, _, loggedKey := bindRequestLogger(rebound, env, "key", "request", "chat")
	require.Equal(t, newKey, loggedKey, "attribution and state reads must use the same namespace")
}

func TestManagedAdmissionIgnoresPersistedBetaPreference(t *testing.T) {
	ctx := router.WithStrategy(context.Background(), router.StrategyHMMEmbedding)
	ctx = requestcontext.WithServingIdentity(ctx, requestcontext.ServingIdentity{StateNamespace: "admitted"})
	svc := (&Service{}).WithSessionStrategyStore(&betaTestPreferenceStore{getErr: context.DeadlineExceeded})
	selected, err := svc.applySessionStrategy(ctx, [16]byte{1}, [16]byte{1})
	require.NoError(t, err, "managed requests must not even read retired beta preferences")
	require.Equal(t, router.StrategyHMMEmbedding, router.StrategyFromContext(selected))
}
