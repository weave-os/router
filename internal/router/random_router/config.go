package randomrouter

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
	"weave-os/router/internal/router/catalog"
)

const PolicyID = "random-no-cache-eviction"
const PolicySHA256 = "99b9bf7642183d5391642460008eaa4ff2713ad0ebbb410d9ac2c4d785e20053"
const PolicyHeader = "X-Weave-Random-Router-Policy"
const ResponsesPath = "/v1/random-router/responses"
const IdentityPath = "/v1/random-router/identity"

func Models() []string {
	return []string{catalog.ModelGPT6Luna, catalog.ModelGPT61Sol.String(), catalog.ModelGPT61Terra.String()}
}

type Config struct {
	APIKeyID              string    `json:"api_key_id"`
	KeySHA256             string    `json:"key_sha256"`
	InstallationID        string    `json:"installation_id"`
	OrganizationID        string    `json:"organization_id"`
	SubscriptionAccountID string    `json:"subscription_account_id"`
	PolicySHA256          string    `json:"policy_sha256"`
	ExpiresAt             time.Time `json:"expires_at"`
}

func ParseConfig(raw string, now time.Time) (*Config, error) {
	if raw == "" {
		return nil, nil
	}
	var config Config
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, errors.New("random benchmark configuration must contain one object")
	}
	for _, id := range []string{config.APIKeyID, config.InstallationID, config.SubscriptionAccountID} {
		if _, err := uuid.Parse(id); err != nil {
			return nil, fmt.Errorf("random benchmark identity: %w", err)
		}
	}
	for _, digest := range []string{config.KeySHA256, config.PolicySHA256} {
		if decoded, err := hex.DecodeString(digest); err != nil || len(decoded) != 32 || strings.ToLower(digest) != digest {
			return nil, errors.New("random benchmark requires lowercase SHA-256 identities")
		}
	}
	if config.OrganizationID == "" || !config.ExpiresAt.After(now) || config.ExpiresAt.After(now.Add(7*24*time.Hour)) {
		return nil, errors.New("random benchmark requires an organization and expiry within seven days")
	}
	if config.PolicySHA256 != PolicySHA256 {
		return nil, errors.New("unsupported random benchmark policy digest")
	}
	return &config, nil
}
