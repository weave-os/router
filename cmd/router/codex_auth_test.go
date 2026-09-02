package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadLocalCodexSubscription(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"oauth-token","account_id":"acct-1"}}`), 0o600))
	t.Setenv("ROUTER_CODEX_AUTH_FILE", path)

	token, accountID := loadLocalCodexSubscription(context.Background())
	assert.Equal(t, "oauth-token", token)
	assert.Equal(t, "acct-1", accountID)
}

func TestLoadLocalCodexSubscriptionRejectsNonChatGPTAuth(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"auth_mode":"apikey","tokens":{"access_token":"oauth-token","account_id":"acct-1"}}`), 0o600))
	t.Setenv("ROUTER_CODEX_AUTH_FILE", path)

	token, accountID := loadLocalCodexSubscription(context.Background())
	assert.Empty(t, token)
	assert.Empty(t, accountID)
}
