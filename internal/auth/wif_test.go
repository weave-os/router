package auth_test

import (
	"testing"

	"weave-os/router/internal/auth"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWIFCredential_UsesProviderPrefixedForm(t *testing.T) {
	credential, err := auth.WIFCredential(auth.WIFProviderGCP, "header.payload.sig")

	require.NoError(t, err)
	assert.Equal(t, "WIF.GCP.header.payload.sig", string(credential),
		"the upstream parses the provider out of the bearer itself; any other shape is rejected before the token is read")
}

func TestWIFCredential_RejectsEmptyAttestation(t *testing.T) {
	for _, token := range []string{"", "   ", "\n"} {
		_, err := auth.WIFCredential(auth.WIFProviderOIDC, token)

		require.ErrorIs(t, err, auth.ErrWIFUnavailable,
			"an empty attestation would dispatch as the literal bearer WIF.OIDC. — a silent 401 per request")
	}
}
