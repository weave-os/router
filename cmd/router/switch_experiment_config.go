package main

import (
	"time"

	"weave-os/router/internal/config"
	"weave-os/router/internal/conversation"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router/handover"
	"weave-os/router/internal/routingpolicy"
)

func switchExperimentConfigFromEnv() proxy.SwitchExperimentConfig {
	return proxy.SwitchExperimentConfig{
		Live: config.GetOr("ROUTER_SWITCH_EXPERIMENT", "") == "live",
		Tuning: routingpolicy.SwitchTuning{
			DowngradeHorizonTurns:  parseEnvInt("ROUTER_SWITCH_DOWNGRADE_HORIZON", 0),
			UpgradeHorizonTurns:    parseEnvInt("ROUTER_SWITCH_UPGRADE_HORIZON", 0),
			CompactMinEvictionUSD:  parseEnvFloat("ROUTER_SWITCH_COMPACT_MIN_EVICTION_USD", 0),
			CompactMinPromptTokens: parseEnvInt("ROUTER_SWITCH_COMPACT_MIN_PROMPT_TOKENS", 0),
		},
		CompactTail:    parseEnvInt("ROUTER_SWITCH_COMPACT_TAIL", 0),
		CompactTimeout: parseEnvDurationMs("ROUTER_SWITCH_COMPACT_TIMEOUT_MS", proxy.DefaultSwitchCompactionTimeout),
		SummaryTokens:  parseEnvInt("ROUTER_SWITCH_COMPACT_SUMMARY_TOKENS", proxy.DefaultSwitchCompactionSummaryTokens),
	}
}

func withSwitchExperiment(svc *proxy.Service, summarizer handover.Summarizer) *proxy.Service {
	compactor, _ := summarizer.(proxy.ThreadCompactor)
	return proxy.EnableSwitchExperiment(svc, switchExperimentConfigFromEnv(), compactor, conversation.NewMemoryCompactionStore(time.Hour))
}
