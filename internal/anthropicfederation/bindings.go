package anthropicfederation

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"strings"

	"github.com/google/uuid"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
)

// ParseInstallationBindings reads operator-owned authorization, independently
// of tenant-supplied provider settings. An empty map authorizes no installation.
func ParseInstallationBindings(raw string) (map[string]auth.AnthropicFederationConfig, error) {
	var bindings map[string]auth.AnthropicFederationConfig
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&bindings); err != nil {
		return nil, fmt.Errorf("invalid Anthropic federation bindings: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("invalid Anthropic federation bindings: expected one JSON object")
	}
	normalized := make(map[string]auth.AnthropicFederationConfig, len(bindings))
	for installationID, target := range bindings {
		parsedID, err := uuid.Parse(installationID)
		if err != nil || parsedID.String() != installationID {
			return nil, fmt.Errorf("Anthropic federation binding must use a canonical installation UUID")
		}
		federation, err := auth.NormalizeAnthropicFederation(auth.AuthTypeAnthropicWIF, providers.ProviderAnthropic, "", &target)
		if err != nil {
			return nil, err
		}
		normalized[installationID] = *federation
	}
	return normalized, nil
}

// RequireInstallationBindings must be called before serving requests. It copies
// the operator's allowlist so tenant configuration and cache entries cannot grant access.
func (s *Source) RequireInstallationBindings(bindings map[string]auth.AnthropicFederationConfig) *Source {
	s.bindings = maps.Clone(bindings)
	if s.bindings == nil {
		s.bindings = map[string]auth.AnthropicFederationConfig{}
	}
	return s
}
