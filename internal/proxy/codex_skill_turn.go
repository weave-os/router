package proxy

import (
	"context"

	"weave-os/router/internal/translate"
)

func codexSkillTurnFrom(ctx context.Context) translate.CodexSkillTurn {
	skill, _ := ctx.Value(codexSkillContextKey{}).(translate.CodexSkillTurn)
	return skill
}

// agentForceModelContinuesTurn reports whether a force-model directive an
// agent emitted mid-turn continues to the model. A Codex force-model skill
// ends its turn once emit.sh prints the directive, so the router answers that
// one like a typed command.
func agentForceModelContinuesTurn(ctx context.Context, cmd translate.ForceModelResult) bool {
	return cmd.FromToolResult && codexSkillTurnFrom(ctx) != translate.CodexSkillTurnForceModel
}
