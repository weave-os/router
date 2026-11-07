package postgres_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tink-crypto/tink-go/v2/aead"
	"github.com/tink-crypto/tink-go/v2/insecurecleartextkeyset"
	"github.com/tink-crypto/tink-go/v2/keyset"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/postgres"
)

func newTinkEncryptor(t *testing.T) auth.Encryptor {
	t.Helper()
	handle, err := keyset.NewHandle(aead.AES256GCMKeyTemplate())
	require.NoError(t, err)
	var buf bytes.Buffer
	require.NoError(t, insecurecleartextkeyset.Write(handle, keyset.NewJSONWriter(&buf)))
	enc, err := auth.NewTinkEncryptor(buf.String())
	require.NoError(t, err)
	return enc
}

// A selfhosted install that stored dashboard keys before a keyset was required
// holds them as plaintext; turning encryption on must not strand them.
func TestEncryptPlaintextKeysUpgradesRowsStoredUnencrypted(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	const externalID = "plaintext-key-upgrade-test"
	t.Cleanup(func() {
		_, err := pool.Exec(ctx, "DELETE FROM router.model_router_installations WHERE external_id = $1", externalID)
		require.NoError(t, err)
	})

	legacy := postgres.NewRepository(pool, auth.NoOpEncryptor{})
	inst, err := legacy.Installations.Create(ctx, auth.CreateInstallationParams{ExternalID: externalID, Name: "plaintext upgrade"})
	require.NoError(t, err)
	_, err = legacy.ExternalAPIKeys.Create(ctx, auth.CreateExternalAPIKeyParams{
		InstallationID: inst.ID,
		ExternalID:     externalID,
		Provider:       "anthropic",
		KeyCiphertext:  []byte("sk-ant-legacy-plaintext"),
		KeyPrefix:      "sk-ant-l",
		KeySuffix:      "text",
		KeyFingerprint: "fp-plaintext-upgrade",
		AuthType:       "bearer",
	})
	require.NoError(t, err)

	enc := newTinkEncryptor(t)
	upgraded := postgres.NewExternalAPIKeyRepo(pool, enc)
	n, err := upgraded.EncryptPlaintextKeys(ctx)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, n, 1)

	var stored []byte
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT key_ciphertext FROM router.model_router_external_api_keys WHERE installation_id = $1", inst.ID).Scan(&stored))
	assert.NotContains(t, string(stored), "sk-ant-legacy-plaintext")

	keys, err := upgraded.GetForInstallation(ctx, inst.ID)
	require.NoError(t, err)
	require.Len(t, keys, 1)
	assert.Equal(t, "sk-ant-legacy-plaintext", string(keys[0].Plaintext))

	again, err := upgraded.EncryptPlaintextKeys(ctx)
	require.NoError(t, err)
	assert.Zero(t, again, "already-encrypted rows must not be rewritten")
}
