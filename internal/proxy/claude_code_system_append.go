package proxy

import (
	"context"

	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/websearch"
)

func claudeCodeSystemAppendApplies(ctx context.Context, enabled bool, body []byte, tt turntype.TurnType, hasSystemText func() bool) bool {
	if !enabled {
		return false
	}
	if requestcontext.ClientIdentityFrom(ctx).ClientApp != requestcontext.ClientAppClaudeCode {
		return false
	}
	if tt != turntype.MainLoop && tt != turntype.ToolResult {
		return false
	}
	if _, searchTurn := websearch.DetectSearchTurn(body); searchTurn {
		return false
	}
	return !hasSystemText()
}
