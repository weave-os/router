package proxy

import (
	"context"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"
	"weave-os/router/internal/websearch"
)

// workspaceSystemAppendApplies mirrors autonomySystemAppendApplies for
// translate.WorkspaceSystemText: Claude Code (or Agent SDK) main-loop and
// tool-result turns only, never the native web-search sub-turn, never a
// request already carrying the instruction. Provider gating is not decided
// here: the cross-format emitters apply the append and PrepareAnthropic
// ignores it, so a sibling failover onto Anthropic serves the client prompt
// untouched.
func (s *Service) workspaceSystemAppendApplies(ctx context.Context, body []byte, env *translate.RequestEnvelope, tt turntype.TurnType) bool {
	if !s.ResolveCCWorkspaceSystemAppend(ctx) {
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
	return !env.HasWorkspaceSystemText()
}

// workspaceAppendFired reports whether the served attempt actually carried
// the append: only the cross-format emitters apply it, so an attempt served
// on an Anthropic-format binding is recorded as not fired.
func workspaceAppendFired(opts translate.EmitOptions, servedProvider string) bool {
	return opts.AppendWorkspaceSystem && providers.FamilyFor(servedProvider) != providers.FamilyAnthropic
}
