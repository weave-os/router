package proxy

import (
	"context"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"
	"weave-os/router/internal/websearch"
)

func (s *Service) autonomySystemAppendApplies(ctx context.Context, body []byte, env *translate.RequestEnvelope, tt turntype.TurnType) bool {
	if !s.ResolveCCAutonomySystemAppend(ctx) {
		return false
	}
	if ClientIdentityFrom(ctx).ClientApp != requestcontext.ClientAppClaudeCode {
		return false
	}
	if tt != turntype.MainLoop && tt != turntype.ToolResult {
		return false
	}
	if _, searchTurn := websearch.DetectSearchTurn(body); searchTurn {
		return false
	}
	return !env.HasAutonomySystemText()
}

func autonomyAppendFired(opts translate.EmitOptions, servedProvider string) bool {
	return opts.AppendAutonomySystem && providers.FamilyFor(servedProvider) != providers.FamilyGemini
}
