package conversation

import (
	"context"

	"weave-os/router/internal/translate"
)

type codexSkillContextKey struct{}

func WithCodexSkillTurn(ctx context.Context, skill translate.CodexSkillTurn) context.Context {
	return context.WithValue(ctx, codexSkillContextKey{}, skill)
}

func CodexSkillTurnFrom(ctx context.Context) translate.CodexSkillTurn {
	skill, _ := ctx.Value(codexSkillContextKey{}).(translate.CodexSkillTurn)
	return skill
}

func AgentForceModelContinuesTurn(ctx context.Context, cmd translate.ForceModelResult) bool {
	return cmd.FromToolResult && CodexSkillTurnFrom(ctx) != translate.CodexSkillTurnForceModel
}
