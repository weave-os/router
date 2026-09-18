package proxy

import (
	"context"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"
	"weave-os/router/internal/websearch"
)

// autonomySystemAppendApplies decides whether this turn's outgoing system
// prompt gets translate.AutonomySystemText appended. Only the turns a human
// would otherwise be expected to answer qualify: Claude Code (or its Agent
// SDK, which identifies the same way) main-loop and tool-result turns. Title
// generation, compaction, probes, classifiers, sub-agent dispatch and the
// self-contained native web-search sub-turn keep their prompts untouched, as
// does any request that already carries the instruction.
func (s *Service) autonomySystemAppendApplies(ctx context.Context, body []byte, env *translate.RequestEnvelope, tt turntype.TurnType) bool {
	if !s.ResolveCCAutonomySystemAppend(ctx) {
		return false
	}
	if ClientIdentityFrom(ctx).ClientApp != ClientAppClaudeCode {
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

// autonomyAppendFired reports whether the served attempt actually carried the
// append. translate.PrepareGemini ignores the option, so a turn that failed
// over onto a Gemini binding is recorded as not fired.
func autonomyAppendFired(opts translate.EmitOptions, servedProvider string) bool {
	return opts.AppendAutonomySystem && providers.FamilyFor(servedProvider) != providers.FamilyGemini
}
