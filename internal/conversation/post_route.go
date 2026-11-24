package conversation

import (
	"context"

	"weave-os/router/internal/router"
	"weave-os/router/internal/routingpolicy"
	"weave-os/router/internal/translate"
)

type PostRouteChecks struct {
	AgentShadowMode bool
	Route           routingpolicy.TurnRoute
	Env             *translate.RequestEnvelope
	ClientBudget    router.ClientBudget
	Scope           router.Request

	RecoverClientCompaction bool
	SummaryMaxTokens        int
	ClientDefaultWindow     int
}

func (c PostRouteChecks) Run(ctx context.Context) error {
	if c.AgentShadowMode || !c.RecoverClientCompaction {
		return nil
	}
	_, err := ApplyClientCompactionRecovery(ctx, c.Env, c.ClientBudget, c.Route.TurnType, c.Scope.ConversationMessages, c.SummaryMaxTokens, c.ClientDefaultWindow)
	return err
}
