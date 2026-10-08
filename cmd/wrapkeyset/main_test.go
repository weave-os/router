package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tink-crypto/tink-go/v2/aead"
	"github.com/tink-crypto/tink-go/v2/insecurecleartextkeyset"
	"github.com/tink-crypto/tink-go/v2/keyset"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/keywrap"
)

func keysetJSON(t *testing.T) string {
	t.Helper()
	handle, err := keyset.NewHandle(aead.AES256GCMKeyTemplate())
	require.NoError(t, err)
	var buf bytes.Buffer
	require.NoError(t, insecurecleartextkeyset.Write(handle, keyset.NewJSONWriter(&buf)))
	return buf.String()
}

func TestRun_WrapsUnderLocalKEK(t *testing.T) {
	kekJSON := keysetJSON(t)
	data := keysetJSON(t)
	t.Setenv("EXTERNAL_KEY_ENCRYPTION_KEK_URI", "")
	t.Setenv("EXTERNAL_KEY_ENCRYPTION_KEK", kekJSON)

	var out bytes.Buffer
	require.NoError(t, run(strings.NewReader(data), &out))

	kek, err := keywrap.NewKEK(keywrap.Config{KEKKeysetJSON: kekJSON})
	require.NoError(t, err)
	wrapped, err := auth.NewWrappedTinkEncryptor(out.String(), kek)
	require.NoError(t, err)
	original, err := auth.NewTinkEncryptor(data)
	require.NoError(t, err)
	ct, err := original.Encrypt([]byte("secret"), "org", "openai")
	require.NoError(t, err)
	pt, err := wrapped.Decrypt(ct, "org", "openai")
	require.NoError(t, err)
	assert.Equal(t, []byte("secret"), pt)
}

func TestRun_RequiresKEK(t *testing.T) {
	t.Setenv("EXTERNAL_KEY_ENCRYPTION_KEK_URI", "")
	t.Setenv("EXTERNAL_KEY_ENCRYPTION_KEK", "")
	err := run(strings.NewReader(keysetJSON(t)), &bytes.Buffer{})
	require.Error(t, err)
}
