package main

import (
	"errors"
	"fmt"
	"strconv"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/server"
)

const allowUnencryptedProviderKeysEnv = "ROUTER_ALLOW_UNENCRYPTED_PROVIDER_KEYS"

// newProviderKeyEncryptor builds the encryptor for stored provider keys.
// Self-hosted installs store operator-entered dashboard keys in their own
// Postgres, so an absent keyset fails boot unless explicitly opted out.
func newProviderKeyEncryptor(mode server.DeploymentMode, lookup func(string) (string, bool)) (auth.Encryptor, error) {
	if keysetJSON, _ := lookup("EXTERNAL_KEY_ENCRYPTION_KEY"); keysetJSON != "" {
		enc, err := auth.NewTinkEncryptor(keysetJSON)
		if err != nil {
			return nil, fmt.Errorf("EXTERNAL_KEY_ENCRYPTION_KEY: %w", err)
		}
		return enc, nil
	}
	if mode != server.DeploymentModeSelfHosted {
		return auth.NoOpEncryptor{}, nil
	}
	raw, _ := lookup(allowUnencryptedProviderKeysEnv)
	if allow, _ := strconv.ParseBool(raw); allow {
		return auth.NoOpEncryptor{}, nil
	}
	return nil, errors.New("EXTERNAL_KEY_ENCRYPTION_KEY is required in selfhosted mode so dashboard provider keys are encrypted at rest; " +
		"generate a keyset with `tinkey create-keyset --key-template AES256_GCM --out-format json` " +
		"(set " + allowUnencryptedProviderKeysEnv + "=true only for local development)")
}
