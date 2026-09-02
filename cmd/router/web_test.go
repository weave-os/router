package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateWebPort(t *testing.T) {
	for _, port := range []string{"1", "8088", "65535"} {
		assert.NoError(t, validateWebPort(port), "port %s should be accepted", port)
	}
	for _, port := range []string{"", "0", "65536", "router"} {
		assert.Error(t, validateWebPort(port), "port %q should be rejected", port)
	}
}

func TestLoadRouterEnvUsesLocalOverrideWithoutOverwritingProcessEnv(t *testing.T) {
	t.Chdir(t.TempDir())
	restoreEnv(t, "ROUTER_TEST_FILE_ENV", "process")
	restoreUnsetEnv(t, "ROUTER_TEST_LOCAL_ENV")
	require.NoError(t, os.WriteFile(".env.development", []byte("ROUTER_TEST_FILE_ENV=development\nROUTER_TEST_LOCAL_ENV=development\n"), 0o600))
	require.NoError(t, os.WriteFile(".env.local", []byte("ROUTER_TEST_FILE_ENV=local\nROUTER_TEST_LOCAL_ENV=local\n"), 0o600))

	require.NoError(t, loadRouterEnv())
	assert.Equal(t, "process", os.Getenv("ROUTER_TEST_FILE_ENV"))
	assert.Equal(t, "local", os.Getenv("ROUTER_TEST_LOCAL_ENV"))
}

func TestWebStateRoundTrip(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	want := webState{
		PID:        1234,
		BinaryPath: "/tmp/router",
		Port:       "8088",
		StartedAt:  time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC),
		LogPath:    "/tmp/router.log",
	}
	require.NoError(t, writeWebState(want))

	got, err := readWebState()
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestReadWebStateRejectsInvalidPID(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	require.NoError(t, writeWebState(webState{PID: -1, Port: "8088"}))

	_, err := readWebState()
	assert.ErrorIs(t, err, errWebStateInvalid)
}

func TestProcessAliveRejectsInvalidPID(t *testing.T) {
	assert.False(t, processAlive(0))
	assert.False(t, processAlive(-1))
}

func TestSignalWebProcessRejectsInvalidPID(t *testing.T) {
	assert.Error(t, signalWebProcess(0))
	assert.Error(t, signalWebProcess(-1))
}

func TestResolveUIAssetsDirUsesInstalledDataPath(t *testing.T) {
	t.Chdir(t.TempDir())
	installed := filepath.Join(t.TempDir(), "ui")
	require.NoError(t, os.MkdirAll(installed, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(installed, "index.html"), []byte("ok"), 0o600))
	original := defaultUIAssetsDir
	t.Cleanup(func() { defaultUIAssetsDir = original })
	defaultUIAssetsDir = installed

	assert.Equal(t, installed, resolveUIAssetsDir())
}

func restoreEnv(t *testing.T, key, value string) {
	t.Helper()
	original, existed := os.LookupEnv(key)
	if err := os.Setenv(key, value); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv(key, original)
		} else {
			_ = os.Unsetenv(key)
		}
	})
}

func restoreUnsetEnv(t *testing.T, key string) {
	t.Helper()
	original, existed := os.LookupEnv(key)
	_ = os.Unsetenv(key)
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv(key, original)
		} else {
			_ = os.Unsetenv(key)
		}
	})
}
