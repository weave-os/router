package proxy

import (
	"context"
	"net/http"

	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/routingpolicy"
	"weave-os/router/internal/translate"
)

type turnCommands struct {
	s                    *Service
	w                    http.ResponseWriter
	env                  *translate.RequestEnvelope
	target               routingpolicy.SessionTarget
	forceModelSessionKey [sessionpin.SessionKeyLen]byte
	inputTokens          int
	requestedModel       string
}

func (c turnCommands) ApplyForceModel(ctx context.Context, cmd translate.ForceModelResult) (string, error) {
	agentForceModel, _, err := (forceModelCommands{c.s}).apply(ctx, c.target.Strategy, cmd, c.target.InstallationID, c.target.SessionKey, c.forceModelSessionKey)
	return agentForceModel, err
}

func (c turnCommands) AcknowledgeForceModel(ctx context.Context, cmd translate.ForceModelResult) error {
	return (forceModelCommands{c.s}).handle(ctx, c.w, c.env, cmd, c.target, c.forceModelSessionKey, c.inputTokens)
}

func (c turnCommands) RecordRouterFeedback(ctx context.Context, cmd translate.RouterFeedbackResult, synthetic bool) error {
	return (routerFeedbackCommand{w: c.w, env: c.env, cmd: cmd, target: c.target, inputTokens: c.inputTokens, synthetic: synthetic}).handle(ctx, c.s)
}

func (c turnCommands) RouterSession(ctx context.Context) error {
	return (routerInfoCommands{c.s}).session(ctx, c.w, c.env, c.inputTokens)
}

func (c turnCommands) RouterModels(ctx context.Context) error {
	return (routerInfoCommands{c.s}).models(ctx, c.w, c.env, c.inputTokens)
}

func (c turnCommands) ListModels(ctx context.Context) error {
	return (routerInfoCommands{c.s}).listModels(ctx, c.w, c.env, c.inputTokens)
}

func (c turnCommands) GrantContinuation(ctx context.Context) {
	postCommandContinuation(c.s, c.target.Strategy).Grant(ctx, c.target.InstallationID, c.target.SessionKey, roleForTier(catalog.TierFor(c.requestedModel)))
}

type chatCommandSource struct {
	*translate.RequestEnvelope
	routing *translate.RequestEnvelope
}

func (c chatCommandSource) ExtractForceModelCommand() (translate.ForceModelResult, bool) {
	cmd, hasCmd := c.RequestEnvelope.ExtractForceModelCommand()
	if hasCmd && c.routing != c.RequestEnvelope {
		c.routing.ExtractForceModelCommand()
	}
	return cmd, hasCmd
}

func (c chatCommandSource) ExtractRouterFeedbackCommand() (translate.RouterFeedbackResult, bool) {
	cmd, hasCmd := c.RequestEnvelope.ExtractRouterFeedbackCommand()
	if hasCmd && c.routing != c.RequestEnvelope {
		c.routing.ExtractRouterFeedbackCommand()
	}
	return cmd, hasCmd
}
