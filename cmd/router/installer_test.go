package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsInstallerInvocation(t *testing.T) {
	for _, args := range [][]string{
		{"install", "--codex"},
		{"--claude"},
		{"--scope", "project"},
		{"--local"},
		{"models", "list", "--codex"},
		{"disable-routing"},
	} {
		assert.True(t, isInstallerInvocation(args), "args %v should use the installer", args)
	}
	assert.False(t, isInstallerInvocation(nil))
	assert.False(t, isInstallerInvocation([]string{"web", "start"}))
}

func TestFindInstallerScriptFromWorkingDirectory(t *testing.T) {
	t.Chdir(t.TempDir())
	original := defaultInstallerDir
	t.Cleanup(func() { defaultInstallerDir = original })
	defaultInstallerDir = ""
	require.NoError(t, os.MkdirAll("install", 0o700))
	require.NoError(t, os.WriteFile(filepath.Join("install", "install.sh"), []byte("#!/usr/bin/env bash\n"), 0o700))

	path, err := findInstallerScript()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(mustGetwd(t), "install", "install.sh"), path)
}

func TestFindInstallerScriptUsesConfiguredDirectory(t *testing.T) {
	configured := t.TempDir()
	path := filepath.Join(configured, "install.sh")
	require.NoError(t, os.WriteFile(path, []byte("#!/usr/bin/env bash\n"), 0o700))
	t.Setenv("ROUTER_INSTALLER_DIR", configured)

	found, err := findInstallerScript()
	require.NoError(t, err)
	assert.Equal(t, path, found)
}

func mustGetwd(t *testing.T) string {
	t.Helper()
	workingDir, err := os.Getwd()
	require.NoError(t, err)
	return workingDir
}
