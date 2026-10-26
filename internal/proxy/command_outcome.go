package proxy

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/google/uuid"

	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/translate"
)

type forceRejection string

const (
	forceUnknownModel     forceRejection = "unknown_model"
	forceExcludedModel    forceRejection = "excluded_model"
	forceInvalidCondition forceRejection = "invalid_condition"
)

type forceAcknowledgment struct {
	rejection    forceRejection
	inputModel   string
	suggestion   string
	reason       string
	clearCommand string
	parentOnly   bool
	group        escalation.Group
}

type feedbackAcknowledgment string

const (
	feedbackRecorded        feedbackAcknowledgment = "recorded"
	feedbackEmpty           feedbackAcknowledgment = "empty"
	feedbackMissingSequence feedbackAcknowledgment = "missing_sequence"
)

// commandAcknowledgment keeps command application facts separate from presentation.
// Pin is assigned only after the owning persistence operation succeeds.
type commandAcknowledgment struct {
	outcome                               translate.CommandOutcome
	force                                 forceAcknowledgment
	sessionID                             string
	clientApp                             string
	routable, excluded, automaticExcluded map[string]struct{}
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
		return routerSessionMessage(ack.sessionID, ack.clientApp, format)
	case translate.CommandRouterModels:
		return routerModelsMessage(ack.routable, ack.excluded, ack.automaticExcluded, directivePrefix(ack.clientApp), format)
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
			return routerFeedbackAck(format, ack.rating)
		}
	}
	return ""
}

func (ack commandAcknowledgment) renderForce(format translate.Format) string {
	switch ack.force.rejection {
	case forceInvalidCondition:
		return "Weave Router: force command rejected: " + ack.force.reason + ". Existing controls are unchanged."
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
	if ack.force.group != "" {
		return fmt.Sprintf("Weave Router: force-model applied: %s (%s), only for %s scored turns. Other groups use automatic routing. Use %s to clear.", model, pin.Provider, ack.force.group, ack.force.clearCommand)
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
	return translate.WriteCommandResponse(w, env, text, inputTokens, outcome, ClientIdentityFrom(ctx).ClientApp == ClientAppCodex)
}

// routerSessionMessage renders the ack in the marker style each surface uses:
// OpenAI-format clients (Codex) get plain text, Anthropic-format clients get
// the ✦ marker the rest of the router's synthetic replies use there.
func routerSessionMessage(sessionID, clientApp string, format translate.Format) string {
	if sessionID == "" {
		// No session header means nothing here is correlated, so naming a
		// substitute id would point support at the wrong session. Say so.
		const detail = "no session id on this request. This client did not send one, so its turns are not correlated in router telemetry."
		if format == translate.FormatOpenAI {
			return "Weave Router: " + detail
		}
		return "✦ **Weave Router** → " + detail + "\n\n"
	}
	hint := directivePrefix(clientApp) + "router-feedback"
	if format == translate.FormatOpenAI {
		return fmt.Sprintf("Weave Router: session id: %s. Quote it in support requests; %s attaches feedback to this session.", sessionID, hint)
	}
	return fmt.Sprintf("✦ **Weave Router** → session id: `%s` · quote it in support requests; `%s` attaches feedback to this session\n\n", sessionID, hint)
}

// routerModelsMessage renders the routable universe grouped by each model's
// primary provider, marking what this installation may actually be routed to.
// The [x]/[ ] form matches what `weave-router models` prints, so the two
// surfaces stay recognizably the same listing.
//
// [-] is the third state, and it exists because the two model-restriction
// layers differ in kind: [ ] is an org exclusion or a model outside the
// allowlist, which nothing can route to, while [-] is a deployment-wide
// automatic exclusion the scorer honours but an explicit force ignores. The
// legend is printed only when at least one model is in that state, so the
// common listing is unchanged.
func routerModelsMessage(routable, excluded, automaticExcluded map[string]struct{}, prefix string, format translate.Format) string {
	if len(routable) == 0 {
		const detail = "no routable models are configured on this router."
		if format == translate.FormatOpenAI {
			return "Weave Router: " + detail
		}
		return "✦ **Weave Router** → " + detail + "\n\n"
	}

	byProvider := make(map[string][]string, len(routable))
	for id := range routable {
		provider := "other"
		if m, ok := catalog.ByID(id); ok {
			if p := m.PrimaryProvider(); p != "" {
				provider = p
			}
		}
		byProvider[provider] = append(byProvider[provider], id)
	}
	providers := make([]string, 0, len(byProvider))
	for p := range byProvider {
		providers = append(providers, p)
	}
	sort.Strings(providers)

	var b strings.Builder
	enabled := 0
	forceOnly := 0
	for _, p := range providers {
		models := byProvider[p]
		sort.Strings(models)
		b.WriteString("\n" + p + "\n")
		for _, id := range models {
			mark := "x"
			switch {
			case contains(excluded, id):
				mark = " "
			case contains(automaticExcluded, id):
				mark = "-"
				forceOnly++
			default:
				enabled++
			}
			b.WriteString(fmt.Sprintf("  [%s] %s\n", mark, id))
		}
	}

	// The mutating subcommands stay on the skill (they need admin auth the
	// chat key deliberately lacks), so the footer has to name the real path
	// rather than imply this directive can change anything.
	footer := fmt.Sprintf(
		"\n%d of %d routable here. Change the selection with `%srouter-models enable <id>` or `%srouter-models disable <id>`.",
		enabled, len(routable), prefix, prefix)
	if forceOnly > 0 {
		footer += fmt.Sprintf(
			"\n[-] means automatic routing is disabled for that model deployment-wide; %sforce-model still serves it.",
			prefix)
	}

	if format == translate.FormatOpenAI {
		return fmt.Sprintf("Weave Router: models this installation can be routed to.\n%s%s", b.String(), footer)
	}
	return fmt.Sprintf("✦ **Weave Router** → models this installation can be routed to\n```\n%s```\n%s\n\n", b.String(), footer)
}

// contains keeps the three-way mark switch readable; a nil map is empty.
func contains(set map[string]struct{}, id string) bool {
	_, ok := set[id]
	return ok
}

// routerFeedbackAck renders the acknowledgment, echoing the verdict. The
// Anthropic-format ack is wrapped as a routing marker so it gets stripped
// from subsequent turns.
func routerFeedbackAck(format translate.Format, rating string) string {
	verdict := ""
	switch rating {
	case translate.RouterFeedbackRatingUp:
		verdict = " 👍"
	case translate.RouterFeedbackRatingDown:
		verdict = " 👎"
	}
	if format == translate.FormatOpenAI {
		return "Weave Router: Feedback recorded" + verdict + ". Thank you."
	}
	return "✦ **Weave Router** → Feedback recorded" + verdict + ". Thank you.\n\n"
}
