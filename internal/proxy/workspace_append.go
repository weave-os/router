package proxy

import (
	"context"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"
)

func (s *Service) workspaceSystemAppendApplies(ctx context.Context, body []byte, env *translate.RequestEnvelope, tt turntype.TurnType) bool {
	return claudeCodeSystemAppendApplies(ctx, s.ResolveCCWorkspaceSystemAppend(ctx), body, tt, env.HasWorkspaceSystemText)
}

func workspaceAppendFired(opts translate.EmitOptions, servedProvider string) bool {
	return opts.AppendWorkspaceSystem && providers.FamilyFor(servedProvider) != providers.FamilyAnthropic
}
