package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

type codexAuthFile struct {
	AuthMode string `json:"auth_mode"`
	Tokens   struct {
		AccessToken string `json:"access_token"`
		AccountID   string `json:"account_id"`
	} `json:"tokens"`
}

// loadLocalCodexSubscription reads the authenticated Codex CLI session. It is
// used only by a local Router process; the token is kept in memory and is
// never logged or copied into Router's HTTP headers.
func loadLocalCodexSubscription(_ context.Context) (string, string) {
	path := os.Getenv("ROUTER_CODEX_AUTH_FILE")
	if path == "" {
		codexHome := os.Getenv("CODEX_HOME")
		if codexHome == "" {
			var err error
			codexHome, err = os.UserHomeDir()
			if err != nil {
				return "", ""
			}
			codexHome = filepath.Join(codexHome, ".codex")
		}
		path = filepath.Join(codexHome, "auth.json")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return "", ""
	}
	var auth codexAuthFile
	if json.Unmarshal(data, &auth) != nil || strings.TrimSpace(auth.AuthMode) != "chatgpt" {
		return "", ""
	}
	return strings.TrimSpace(auth.Tokens.AccessToken), strings.TrimSpace(auth.Tokens.AccountID)
}
