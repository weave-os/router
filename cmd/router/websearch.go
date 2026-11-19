package main

import (
	"log/slog"

	"weave-os/router/internal/config"
	"weave-os/router/internal/providers/cortexagents"
	"weave-os/router/internal/providers/keenable"
	"weave-os/router/internal/websearch"
)

func cortexWebSearch(logger *slog.Logger) websearch.Executor {
	if config.GetOr("ROUTER_CORTEX_WEB_SEARCH", "true") != "true" {
		logger.Info("Cortex Agents web-search executor disabled (ROUTER_CORTEX_WEB_SEARCH)")
		return nil
	}
	role := config.GetOr("SNOWFLAKE_AGENT_ROLE", "")
	hostSuffix := config.GetOr("SNOWFLAKE_AGENT_HOST_SUFFIX", "")
	timeout := parseEnvDurationMs("SNOWFLAKE_AGENT_TIMEOUT_MS", 0)
	logger.Info("Cortex Agents web-search executor enabled",
		"snowflake_role", role, "host_suffix_override", hostSuffix,
		"timeout_ms", timeout.Milliseconds())
	opts := []cortexagents.Option{cortexagents.WithRole(role), cortexagents.WithTimeout(timeout)}
	if hostSuffix != "" {
		opts = append(opts, cortexagents.WithHostSuffix(hostSuffix))
	}
	return cortexagents.NewClient(config.GetOr("ANTHROPIC_GATEWAY_BASE_URL", ""), opts...)
}

func keenableWebSearch(logger *slog.Logger) websearch.Executor {
	if config.GetOr("ROUTER_KEENABLE_WEB_SEARCH", "false") != "true" {
		return nil
	}
	client := keenable.NewClient(config.GetOr("KEENABLE_BASE_URL", keenable.DefaultBaseURL), keenable.WithAPIKey(config.GetOr("KEENABLE_API_KEY", "")))
	logger.Info("Keenable web-search fallback enabled", "key_configured", !client.Keyless())
	return client
}
