package wif_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/wif"
)

func TestFileIdentityTokenSourceReadsRotationWithoutSnowflakeEnvelope(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oidc-token")
	source := wif.NewFileIdentityTokenSource(path)
	for _, raw := range []string{"first.jwt.signature", "rotated.jwt.signature"} {
		require.NoError(t, os.WriteFile(path, []byte("\n"+raw+"\n"), 0600))
		token, err := source.IdentityToken(context.Background())
		require.NoError(t, err)
		assert.Equal(t, raw, string(token))
	}
	require.NoError(t, os.WriteFile(path, []byte(" \n"), 0600))
	_, err := source.IdentityToken(context.Background())
	require.Error(t, err)
}
