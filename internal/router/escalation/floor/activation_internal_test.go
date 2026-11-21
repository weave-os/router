package floor

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/requestcontext"
)

func TestEscalationActivationPreservesLegacyIdentityAndIsolatesManagedState(t *testing.T) {
	installationID := uuid.New()
	const material = "credential/strategy/active/1"
	mac := hmac.New(sha256.New, installationID[:])
	_, _ = mac.Write([]byte(material))
	legacy := ActivationID(context.Background(), installationID, material)
	require.Equal(t, mac.Sum(nil), legacy[:], "legacy continuation lookup must keep its existing identity")
	old := requestcontext.WithServingIdentity(context.Background(), requestcontext.ServingIdentity{StateNamespace: "release-a-generation-1"})
	rebound := requestcontext.WithServingIdentity(context.Background(), requestcontext.ServingIdentity{StateNamespace: "release-a-generation-2"})
	oldActivation := ActivationID(old, installationID, material)
	require.NotEqual(t, legacy, oldActivation)
	require.NotEqual(t, oldActivation, ActivationID(rebound, installationID, material))
	require.Equal(t, oldActivation, ActivationID(old, installationID, material))
}
