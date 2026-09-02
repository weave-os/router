package codex

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOAuthManagerStartsCodexAppServerLogin(t *testing.T) {
	command := filepath.Join(t.TempDir(), "codex")
	script := "#!/bin/sh\nwhile IFS= read -r line; do\n  case \"$line\" in\n    *account/login/start*)\n      printf '%s\\n' '{\"id\":2,\"result\":{\"type\":\"chatgpt\",\"loginId\":\"login-1\",\"authUrl\":\"https://auth.example.test/login\"}}'\n      ;;\n  esac\ndone\n"
	require.NoError(t, os.WriteFile(command, []byte(script), 0o700))
	t.Setenv("ROUTER_CODEX_BIN", command)
	t.Setenv("ROUTER_CODEX_AUTH_FILE", filepath.Join(t.TempDir(), "auth.json"))

	manager := NewOAuthManager()
	start, err := manager.Start(context.Background())
	require.NoError(t, err)
	require.Equal(t, "login-1", start.LoginID)
	require.Equal(t, "https://auth.example.test/login", start.AuthURL)
	require.Equal(t, statePending, manager.Status(context.Background()).State)

	require.NoError(t, manager.Cancel(context.Background()))
	require.Equal(t, stateIdle, manager.Status(context.Background()).State)
}

func TestOAuthManagerReadsCodexAuthModeWithoutReturningCredentials(t *testing.T) {
	authFile := filepath.Join(t.TempDir(), "auth.json")
	require.NoError(t, os.WriteFile(authFile, []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"secret"}}`), 0o600))
	t.Setenv("ROUTER_CODEX_AUTH_FILE", authFile)

	manager := NewOAuthManager()
	status := manager.Status(context.Background())
	require.Equal(t, stateAuthenticated, status.State)
	require.Empty(t, status.LoginID)
	require.Empty(t, status.AuthURL)
}
