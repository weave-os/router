package proxy

import (
	"context"

	"weave-os/router/internal/translate"
)

func codexSkillTurnFrom(ctx context.Context) translate.CodexSkillTurn {
	skill, _ := ctx.Value(codexSkillContextKey{}).(translate.CodexSkillTurn)
	return skill
}

func agentForceModelContinuesTurn(ctx context.Context, cmd translate.ForceModelResult) bool {
	return cmd.FromToolResult && codexSkillTurnFrom(ctx) != translate.CodexSkillTurnForceModel
}
