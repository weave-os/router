package proxy

import (
	"context"
	"fmt"
	"net/http"

	"github.com/google/uuid"

	"weave-os/router/internal/conversation"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/translate"
)

type forceRejection string

const (
	forceUnknownModel  forceRejection = "unknown_model"
	forceExcludedModel forceRejection = "excluded_model"
)

type forceAcknowledgment struct {
	rejection    forceRejection
	inputModel   string
	suggestion   string
	reason       string
	clearCommand string
	parentOnly   bool
}

type feedbackAcknowledgment string

const (
	feedbackRecorded        feedbackAcknowledgment = "recorded"
	feedbackEmpty           feedbackAcknowledgment = "empty"
	feedbackMissingSequence feedbackAcknowledgment = "missing_sequence"
)

type commandAcknowledgment struct {
	outcome                               translate.CommandOutcome
	force                                 forceAcknowledgment
	sessionID                             string
	clientApp                             string
	routable, excluded, automaticExcluded map[string]struct{}
	forceable                             []conversation.ForceableModel
	feedback                              feedbackAcknowledgment
	rating                                string
}

func newCommandAcknowledgment(kind translate.CommandKind) commandAcknowledgment {
	return commandAcknowledgment{outcome: translate.CommandOutcome{
		Version: 1, ID: uuid.NewString(), Command: kind, Disposition: translate.CommandAccepted,
		Pin: translate.CommandPin{Transition: translate.PinUnchanged},
	}}
}

func (ack commandAcknowledgment) render(format translate.Format) string {
	switch ack.outcome.Command {
	case translate.CommandForceModel:
		return ack.renderForce(format)
	case translate.CommandRouterSession:
		return conversation.RouterSessionMessage(ack.sessionID, directivePrefix(ack.clientApp), format)
	case translate.CommandRouterModels:
		return conversation.RouterModelsMessage(ack.routable, ack.excluded, ack.automaticExcluded, directivePrefix(ack.clientApp), format)
	case translate.CommandListModels:
		return conversation.ListModelsMessage(ack.forceable, directivePrefix(ack.clientApp), format)
	case translate.CommandRouterFeedback:
		rf := directivePrefix(ack.clientApp) + "rf"
		switch ack.feedback {
		case feedbackEmpty:
			if format == translate.FormatOpenAI {
				return fmt.Sprintf("Weave Router: router-feedback needs a verdict or a note, e.g. %s+ or %s- too slow.", rf, rf)
			}
			return fmt.Sprintf("✦ **Weave Router** → Router-feedback needs a verdict or a note, e.g. %s+ or %s- too slow.\n\n", rf, rf)
		case feedbackMissingSequence:
			if format == translate.FormatOpenAI {
				return fmt.Sprintf("Weave Router: No turn found at that sequence number. Try `%s` without a number for the last turn.", rf)
			}
			return fmt.Sprintf("✦ **Weave Router** → No turn found at that sequence number. Try `%s` without a number for the last turn.\n\n", rf)
		case feedbackRecorded:
			return conversation.RouterFeedbackAck(format, ack.rating)
		}
	case translate.CommandLoopStop:
	}
	return ""
}

func (ack commandAcknowledgment) renderForce(format translate.Format) string {
	switch ack.force.rejection {
	case forceUnknownModel:
		hint := "Use a model ID or shorthand, e.g. " + forceModelExamples + "."
		if ack.force.suggestion != "" {
			hint = fmt.Sprintf("Did you mean %q?", ack.force.suggestion)
		}
		if format == translate.FormatOpenAI {
			return fmt.Sprintf("Weave Router: force-model: %q isn't a recognized model; keeping automatic routing. %s", ack.force.inputModel, hint)
		}
		return fmt.Sprintf("✦ **Weave Router** → force-model: %q isn't a recognized model · keeping automatic routing. %s\n\n", ack.force.inputModel, hint)
	case forceExcludedModel:
		if format == translate.FormatOpenAI {
			return fmt.Sprintf("Weave Router: force-model rejected: %s; keeping automatic routing. Ask an admin to allow the provider, or force a model from one that is permitted.", ack.force.reason)
		}
		return fmt.Sprintf("✦ **Weave Router** → force-model rejected: %s · keeping automatic routing. Ask an admin to allow the provider, or force a model from one that is permitted.\n\n", ack.force.reason)
	}
	pin := ack.outcome.Pin
	if pin.Transition == translate.PinCleared {
		if format == translate.FormatOpenAI {
			return "Weave Router: force-model cleared; resuming automatic model selection"
		}
		return "✦ **Weave Router** → force-model cleared · resuming automatic model selection\n\n"
	}
	model := pin.Model
	if pin.Effort != "" {
		model += ":" + pin.Effort
	}
	if ack.force.parentOnly {
		if format == translate.FormatOpenAI {
			return fmt.Sprintf("Weave Router: force-parent applied: %s (%s). Subagents use automatic routing. Use %s to clear.", model, pin.Provider, ack.force.clearCommand)
		}
		return fmt.Sprintf("✦ **Weave Router** → force-parent applied: %s (%s) · Subagents use automatic routing · Use %s to clear\n\n", model, pin.Provider, ack.force.clearCommand)
	}
	if format == translate.FormatOpenAI {
		return fmt.Sprintf("Weave Router: force-model applied: %s (%s). Use %s to clear.", model, pin.Provider, ack.force.clearCommand)
	}
	return fmt.Sprintf("✦ **Weave Router** → force-model applied: %s (%s) · Use %s to clear\n\n", model, pin.Provider, ack.force.clearCommand)
}

func writeCommandAcknowledgment(ctx context.Context, w http.ResponseWriter, env *translate.RequestEnvelope, ack commandAcknowledgment, inputTokens int) error {
	return writeCommandOutcome(ctx, w, env, ack.render(env.SourceFormat()), ack.outcome, inputTokens)
}

func writeCommandOutcome(ctx context.Context, w http.ResponseWriter, env *translate.RequestEnvelope, text string, outcome translate.CommandOutcome, inputTokens int) error {
	return translate.WriteCommandResponse(w, env, text, inputTokens, outcome, ClientIdentityFrom(ctx).ClientApp == requestcontext.ClientAppCodex)
}
