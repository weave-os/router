package conversation

import (
	"context"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/translate"
)

type CommandSource interface {
	ExtractForceModelCommand() (translate.ForceModelResult, bool)
	ExtractRouterFeedbackCommand() (translate.RouterFeedbackResult, bool)
	ExtractRouterSessionCommand() bool
	ExtractRouterModelsCommand() bool
	ExtractListModelsCommand() bool
}

type CommandHandlers interface {
	ApplyForceModel(ctx context.Context, cmd translate.ForceModelResult) (string, error)
	AcknowledgeForceModel(ctx context.Context, cmd translate.ForceModelResult) error
	RecordRouterFeedback(ctx context.Context, cmd translate.RouterFeedbackResult, synthetic bool) error
	RouterSession(ctx context.Context) error
	RouterModels(ctx context.Context) error
	ListModels(ctx context.Context) error
	GrantContinuation(ctx context.Context)
}

type CommandDispatch struct {
	Source                  CommandSource
	Handlers                CommandHandlers
	Protocol                string
	ForceModelEnabled       bool
	ForceModelContinuesTurn func(translate.ForceModelResult) bool
	SyntheticFeedback       func(translate.RouterFeedbackResult) bool
}

type CommandOutcome struct {
	Handled         bool
	AgentForceModel string
	BodyChanged     bool
}

func (d CommandDispatch) Run(ctx context.Context) (CommandOutcome, error) {
	var out CommandOutcome
	if d.ForceModelEnabled {
		if cmd, hasCmd := d.Source.ExtractForceModelCommand(); hasCmd {
			if err := d.forceModel(ctx, cmd, &out); err != nil || out.Handled {
				return out, err
			}
		}
	}
	if cmd, hasCmd := d.Source.ExtractRouterFeedbackCommand(); hasCmd {
		if err := d.routerFeedback(ctx, cmd, &out); err != nil || out.Handled {
			return out, err
		}
	}
	return d.routerInfo(ctx, out)
}

func (d CommandDispatch) forceModel(ctx context.Context, cmd translate.ForceModelResult, out *CommandOutcome) error {
	observability.FromContext(ctx).Info(d.Protocol+" force-model command", "force_model_cmd", cmd)
	if d.ForceModelContinuesTurn(cmd) {
		agentForceModel, err := d.Handlers.ApplyForceModel(ctx, cmd)
		if err != nil {
			return err
		}
		out.AgentForceModel, out.BodyChanged = agentForceModel, true
		return nil
	}
	if err := d.Handlers.AcknowledgeForceModel(ctx, cmd); err != nil {
		return err
	}
	d.Handlers.GrantContinuation(ctx)
	out.Handled = true
	return nil
}

func (d CommandDispatch) routerFeedback(ctx context.Context, cmd translate.RouterFeedbackResult, out *CommandOutcome) error {
	observability.FromContext(ctx).Info(d.Protocol + " router-feedback command")
	synthetic := d.SyntheticFeedback(cmd)
	if err := d.Handlers.RecordRouterFeedback(ctx, cmd, synthetic); err != nil {
		return err
	}
	if synthetic {
		d.Handlers.GrantContinuation(ctx)
		out.Handled = true
		return nil
	}
	out.BodyChanged = true
	return nil
}

func (d CommandDispatch) routerInfo(ctx context.Context, out CommandOutcome) (CommandOutcome, error) {
	var name string
	var answer func(context.Context) error
	switch {
	case d.Source.ExtractRouterSessionCommand():
		name, answer = "router-session", d.Handlers.RouterSession
	case d.Source.ExtractRouterModelsCommand():
		name, answer = "router-models", d.Handlers.RouterModels
	case d.Source.ExtractListModelsCommand():
		name, answer = "list-models", d.Handlers.ListModels
	default:
		return out, nil
	}
	observability.FromContext(ctx).Info(d.Protocol + " " + name + " command")
	if err := answer(ctx); err != nil {
		return out, err
	}
	d.Handlers.GrantContinuation(ctx)
	out.Handled = true
	return out, nil
}
