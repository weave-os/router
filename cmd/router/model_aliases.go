package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"workweave/router/internal/config"
	"workweave/router/internal/providers"
	"workweave/router/internal/router/catalog"
)

// aliasableProviders are the OpenAI-compatible upstreams whose outbound model
// ID can be rewritten before dispatch.
var aliasableProviders = []string{
	providers.ProviderOpenRouter,
	providers.ProviderFireworks,
	providers.ProviderMakora,
	providers.ProviderTogether,
	providers.ProviderXAI,
	providers.ProviderBedrock,
}

// modelAliasEnvVar names the operator override for a provider's outbound model IDs.
func modelAliasEnvVar(provider string) string {
	return "ROUTER_" + strings.ToUpper(provider) + "_MODEL_ALIASES"
}

// resolveModelAliases returns each aliasable provider's public model ID ->
// upstream model ID map: the catalog's UpstreamID bindings with that provider's
// ROUTER_<PROVIDER>_MODEL_ALIASES entries layered on top.
func resolveModelAliases(logger *slog.Logger) (map[string]map[string]string, error) {
	out := make(map[string]map[string]string, len(aliasableProviders))
	for _, provider := range aliasableProviders {
		envVar := modelAliasEnvVar(provider)
		overrides, err := parseModelAliases(config.GetOr(envVar, ""))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", envVar, err)
		}
		merged := upstreamIDsForProvider(provider)
		for id, upstreamID := range overrides {
			if _, known := catalog.ByID(id); !known {
				// Retiring a catalog model must not turn a stale alias into a boot failure.
				logger.Warn("Ignoring model alias for unknown catalog model", "env_var", envVar, "model", id)
				continue
			}
			if merged == nil {
				merged = make(map[string]string, len(overrides))
			}
			merged[id] = upstreamID
		}
		if len(merged) > 0 {
			out[provider] = merged
		}
	}
	return out, nil
}

// parseModelAliases decodes a JSON object of public model ID -> upstream model
// ID. An empty value yields no entries.
func parseModelAliases(raw string) (map[string]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var aliases map[string]string
	if err := json.Unmarshal([]byte(raw), &aliases); err != nil {
		return nil, fmt.Errorf("expected a JSON object mapping model ID to upstream model ID: %w", err)
	}
	for id, upstreamID := range aliases {
		if strings.TrimSpace(id) == "" {
			return nil, errors.New("empty model ID")
		}
		if strings.TrimSpace(upstreamID) == "" {
			return nil, fmt.Errorf("model %q maps to an empty upstream model ID", id)
		}
	}
	return aliases, nil
}

// upstreamIDsForProvider maps public model ID -> upstream model ID for a
// provider's bindings with a non-empty UpstreamID; nil if no rewriting is
// needed (e.g. OpenRouter, where the slug IS the upstream ID).
func upstreamIDsForProvider(provider string) map[string]string {
	out := make(map[string]string)
	for _, m := range catalog.Models {
		for _, b := range m.Providers {
			if b.Provider == provider && b.UpstreamID != "" {
				out[m.ID] = b.UpstreamID
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
