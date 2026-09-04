package wif_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/wif"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeToken(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
	return path
}

func TestFileTokenSource_FormatsProjectedToken(t *testing.T) {
	src := wif.NewFileTokenSource(writeToken(t, "header.payload.sig\n"))

	credential, err := src.Attestation(context.Background())

	require.NoError(t, err)
	assert.Equal(t, "WIF.OIDC.header.payload.sig", string(credential),
		"a trailing newline from the projecting runtime must not reach the upstream inside the bearer")
}

func TestFileTokenSource_RereadsRotatedToken(t *testing.T) {
	path := writeToken(t, "first")
	src := wif.NewFileTokenSource(path)
	_, err := src.Attestation(context.Background())
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(path, []byte("second"), 0o600))
	credential, err := src.Attestation(context.Background())

	require.NoError(t, err)
	assert.Equal(t, "WIF.OIDC.second", string(credential),
		"the runtime rewrites the token before expiry; caching the first read would authenticate with an expired one")
}

func TestFileTokenSource_EmptyFileIsNotACredential(t *testing.T) {
	src := wif.NewFileTokenSource(writeToken(t, "\n"))

	_, err := src.Attestation(context.Background())

	require.ErrorIs(t, err, auth.ErrWIFUnavailable)
}

func TestFileTokenSource_MissingFile(t *testing.T) {
	src := wif.NewFileTokenSource(filepath.Join(t.TempDir(), "absent"))

	_, err := src.Attestation(context.Background())

	require.Error(t, err)
}

func TestFileTokenSource_HonoursCancelledContext(t *testing.T) {
	src := wif.NewFileTokenSource(writeToken(t, "header.payload.sig"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := src.Attestation(ctx)

	require.ErrorIs(t, err, context.Canceled,
		"credential resolution runs on the request path — an abandoned request must not keep doing work")
}
