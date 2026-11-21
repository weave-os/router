package proxy

import (
	"log/slog"

	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate/toolcheck"
)

func logToolCallIssues(log *slog.Logger, issues []toolcheck.Issue, model, provider string, sessionKey [sessionpin.SessionKeyLen]byte) {
	for _, iss := range issues {
		log.Info("router.tool_call_invalid",
			"tool_name", iss.ToolName,
			"failure_bucket", string(iss.Bucket),
			"detail", iss.Detail,
			"repaired", iss.Repaired,
			"repair_actions", iss.Actions,
			"model", model,
			"provider", provider,
			"session_key_prefix", shortSessionKey(sessionKey),
		)
	}
}
