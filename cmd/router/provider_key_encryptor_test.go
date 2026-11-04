package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tink-crypto/tink-go/v2/aead"
	"github.com/tink-crypto/tink-go/v2/insecurecleartextkeyset"
	"github.com/tink-crypto/tink-go/v2/keyset"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/server"
)

func lookupFrom(env map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := env[k]
		return v, ok
	}
}

func testKeyset(t *testing.T) string {
	t.Helper()
	handle, err := keyset.NewHandle(aead.AES256GCMKeyTemplate())
	require.NoError(t, err)
	var buf bytes.Buffer
	require.NoError(t, insecurecleartextkeyset.Write(handle, keyset.NewJSONWriter(&buf)))
	return buf.String()
}

func TestProviderKeyEncryptor_SelfHostedRequiresKeyset(t *testing.T) {
	_, err := newProviderKeyEncryptor(server.DeploymentModeSelfHosted, lookupFrom(nil))
	require.ErrorContains(t, err, "EXTERNAL_KEY_ENCRYPTION_KEY")

	enc, err := newProviderKeyEncryptor(server.DeploymentModeSelfHosted, lookupFrom(map[string]string{
		"EXTERNAL_KEY_ENCRYPTION_KEY": testKeyset(t),
	}))
	require.NoError(t, err)
	ciphertext, err := enc.Encrypt([]byte("sk-ant-secret"), "inst", "anthropic")
	require.NoError(t, err)
	assert.NotContains(t, string(ciphertext), "sk-ant-secret")
}

func TestProviderKeyEncryptor_UnencryptedNeedsExplicitOptOut(t *testing.T) {
	enc, err := newProviderKeyEncryptor(server.DeploymentModeSelfHosted, lookupFrom(map[string]string{
		"ROUTER_ALLOW_UNENCRYPTED_PROVIDER_KEYS": "true",
	}))
	require.NoError(t, err)
	assert.IsType(t, auth.NoOpEncryptor{}, enc)

	enc, err = newProviderKeyEncryptor(server.DeploymentModeManaged, lookupFrom(nil))
	require.NoError(t, err)
	assert.IsType(t, auth.NoOpEncryptor{}, enc)
}

func TestProviderKeyEncryptor_MalformedKeysetFails(t *testing.T) {
	_, err := newProviderKeyEncryptor(server.DeploymentModeManaged, lookupFrom(map[string]string{
		"EXTERNAL_KEY_ENCRYPTION_KEY": "{not a keyset",
	}))
	require.Error(t, err)
}
