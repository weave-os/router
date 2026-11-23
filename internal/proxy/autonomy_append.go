package proxy

import (
	"context"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"
)

func (s *Service) autonomySystemAppendApplies(ctx context.Context, body []byte, env *translate.RequestEnvelope, tt turntype.TurnType) bool {
	return claudeCodeSystemAppendApplies(ctx, s.ingress.AutonomySystemAppend(ctx), body, tt, env.HasAutonomySystemText)
}

func autonomyAppendFired(opts translate.EmitOptions, servedProvider string) bool {
	return opts.AppendAutonomySystem && providers.FamilyFor(servedProvider) != providers.FamilyGemini
}
