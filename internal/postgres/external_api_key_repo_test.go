package postgres_test

import (
	"bytes"
	"context"
	"testing"

	"workweave/router/internal/auth"
	"workweave/router/internal/postgres"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tink-crypto/tink-go/v2/aead"
	"github.com/tink-crypto/tink-go/v2/insecurecleartextkeyset"
	"github.com/tink-crypto/tink-go/v2/keyset"
)

// Workload-identity rows are written with an empty key_ciphertext (the column is
// NOT NULL and the credential is minted per request), which a real AEAD refuses
// to decrypt. Because one bad row aborts the whole read, that used to hide every
// BYOK key on the installation. Gated on ROUTER_TEST_DATABASE_URL like the other
// database-backed tests in this package.
func TestGetForInstallationLoadsWIFKeyWithoutCiphertext(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	var installationID string
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO model_router_installations (external_id, name) VALUES ($1, $2) RETURNING id`,
		"org_wif_test", t.Name(),
	).Scan(&installationID))
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), `DELETE FROM model_router_installations WHERE id = $1`, installationID)
		assert.NoError(t, err)
	})

	encryptor := newTestEncryptor(t)
	bearerCiphertext, err := encryptor.Encrypt([]byte("sk-secret"), "org_wif_test", "openai")
	require.NoError(t, err)

	_, err = pool.Exec(ctx,
		`INSERT INTO model_router_external_api_keys
		   (installation_id, external_id, provider, key_ciphertext, key_prefix, key_suffix, key_fingerprint, auth_type)
		 VALUES ($1, $2, 'openai_gateway', ''::bytea, 'wif', 'wif', 'fp-wif', 'wif'),
		        ($1, $2, 'openai', $3, 'sk-', 'cret', 'fp-bearer', 'bearer')`,
		installationID, "org_wif_test", bearerCiphertext,
	)
	require.NoError(t, err)

	keys, err := postgres.NewExternalAPIKeyRepo(pool, encryptor).GetForInstallation(ctx, installationID)
	require.NoError(t, err)
	require.Len(t, keys, 2)

	byProvider := make(map[string]*auth.ExternalAPIKey, len(keys))
	for _, key := range keys {
		byProvider[key.Provider] = key
	}
	require.Contains(t, byProvider, "openai_gateway")
	assert.Equal(t, auth.AuthTypeWIF, byProvider["openai_gateway"].AuthType)
	assert.Empty(t, byProvider["openai_gateway"].Plaintext)
	assert.Equal(t, []byte("sk-secret"), byProvider["openai"].Plaintext)
}

func newTestEncryptor(t *testing.T) auth.Encryptor {
	t.Helper()

	handle, err := keyset.NewHandle(aead.AES256GCMKeyTemplate())
	require.NoError(t, err)

	var buf bytes.Buffer
	require.NoError(t, insecurecleartextkeyset.Write(handle, keyset.NewJSONWriter(&buf)))

	encryptor, err := auth.NewTinkEncryptor(buf.String())
	require.NoError(t, err)
	return encryptor
}
