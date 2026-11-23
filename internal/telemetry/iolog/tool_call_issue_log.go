package iolog

import (
	"log/slog"

	"weave-os/router/internal/translate/toolcheck"
)

func LogToolCallIssues(log *slog.Logger, issues []toolcheck.Issue, model, provider, sessionKeyPrefix string) {
	for _, iss := range issues {
		log.Info("router.tool_call_invalid",
			"tool_name", iss.ToolName,
			"failure_bucket", string(iss.Bucket),
			"detail", iss.Detail,
			"repaired", iss.Repaired,
			"repair_actions", iss.Actions,
			"model", model,
			"provider", provider,
			"session_key_prefix", sessionKeyPrefix,
		)
	}
}
