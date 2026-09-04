package proxy

import (
	"context"
	"strings"

	"workweave/router/internal/observability/otel"
	"workweave/router/internal/router"
	"workweave/router/internal/translate"
)

// Effort sources, ordered by the precedence resolveEffort applies.
const (
	effortSourceUser        = "user"
	effortSourceEscalation  = "escalation"
	effortSourceArm         = "arm"
	effortSourceModelPolicy = "model_policy"
)

// effortResolution is one dispatch target's effort: the level routing selected
// and the level the emit path actually writes on the wire, which differ when
// the target's menu can't express the selection (xhigh → max → high).
type effortResolution struct {
	Selected string
	Sent     string
	Source   string
}

// Mismatch reports whether the wire level differs from the selected one. A
// policy arm labelled with the selected level did not buy what it was charged
// for, so such a turn must not train the policy.
func (e effortResolution) Mismatch() bool {
	return e.Selected != "" && e.Selected != e.Sent
}

// apply writes the resolution onto emit options. ForceEffort carries the
// pre-cap level (the Anthropic adaptive seam applies its own cap); the
// escalation and model-policy levels are wire levels only.
func (e effortResolution) apply(opts *translate.EmitOptions) {
	switch e.Source {
	case effortSourceUser, effortSourceArm:
		opts.ForceEffort = e.Selected
		opts.ForceReasoningEffort = translate.ResolveForceEffort(opts.Capabilities, e.Selected)
	case effortSourceEscalation, effortSourceModelPolicy:
		opts.ForceEffort = ""
		opts.ForceReasoningEffort = e.Sent
	default:
		opts.ForceEffort = ""
		opts.ForceReasoningEffort = ""
	}
}

// applyEffortAttrs stamps the effort actually dispatched with, alongside the
// level routing selected and where it came from.
func applyEffortAttrs(b *otel.AttrBuilder, e effortResolution) *otel.AttrBuilder {
	return b.String("routing.selected_effort", e.Selected).
		String("routing.sent_effort", e.Sent).
		String("routing.effort_source", e.Source).
		Bool("routing.effort_mismatch", e.Mismatch())
}

// resolveEffort resolves this dispatch target's effort once, in precedence
// order: user override > failure escalation > policy arm > per-model default.
// Escalation outranks the arm because it exists to rescue a turn the arm's
// level already failed.
func (s *Service) resolveEffort(ctx context.Context, decision router.Decision, caps router.ModelSpec, escalate bool) effortResolution {
	escalationAllowed := s.ResolveEffortEscalation(ctx) || strings.HasPrefix(decision.Model, "grok-")
	if knobs := routingKnobsForRequest(ctx); knobs != nil && knobs.ForceEffort != "" {
		return effortResolutionFor(caps, knobs.ForceEffort, effortSourceUser)
	}
	if escalate && escalationAllowed {
		if level := forcedReasoningEffort(decision.Model, true); level != "" {
			return effortResolutionFor(caps, level, effortSourceEscalation)
		}
	}
	if decision.Effort != "" {
		return effortResolutionFor(caps, decision.Effort, effortSourceArm)
	}
	if escalationAllowed {
		if level := forcedReasoningEffort(decision.Model, false); level != "" {
			return effortResolutionFor(caps, level, effortSourceModelPolicy)
		}
	}
	return effortResolution{}
}

func effortResolutionFor(caps router.ModelSpec, level, source string) effortResolution {
	selected := router.CanonicalizeEffort(level)
	sent := translate.ClampReasoningLevel(caps, translate.ResolveForceEffort(caps, level))
	if sent == "" {
		return effortResolution{}
	}
	return effortResolution{Selected: selected, Sent: sent, Source: source}
}
