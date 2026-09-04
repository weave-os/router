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

// effortResolution is one dispatch target's effort: the arm's own level, the
// level that won precedence, and the level the emit path actually writes on
// the wire, which differ when an override outranks the arm or the target's
// menu can't express the selection (xhigh → max → high).
type effortResolution struct {
	Arm      string
	Selected string
	Sent     string
	Source   string
}

// Mismatch reports whether the wire level differs from the one the policy arm
// is labelled with. An override or a clamp credits that arm with a level it
// never bought, so such a turn must not train the policy.
func (e effortResolution) Mismatch() bool {
	if e.Arm != "" {
		return e.Arm != e.Sent
	}
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
	return b.String("routing.arm_effort", e.Arm).
		String("routing.selected_effort", e.Selected).
		String("routing.sent_effort", e.Sent).
		String("routing.effort_source", e.Source).
		Bool("routing.effort_mismatch", e.Mismatch())
}

// resolveEffort resolves this dispatch target's effort once, in precedence
// order: user override > failure escalation > policy arm > per-model default.
// Escalation outranks the arm because it exists to rescue a turn the arm's
// level already failed.
func (s *Service) resolveEffort(ctx context.Context, decision router.Decision, caps router.ModelSpec, escalate bool) effortResolution {
	arm := router.CanonicalizeEffort(decision.Effort)
	escalationAllowed := s.ResolveEffortEscalation(ctx) || strings.HasPrefix(decision.Model, "grok-")
	if knobs := routingKnobsForRequest(ctx); knobs != nil && knobs.ForceEffort != "" {
		return effortResolutionFor(caps, arm, knobs.ForceEffort, effortSourceUser)
	}
	if escalate && escalationAllowed {
		// Escalation rescues a turn the arm's level already failed, so it may
		// only raise the level: the per-model escalation default is a fixed
		// "high"/"low" that would otherwise downgrade a richer arm.
		if level := router.HigherEffort(forcedReasoningEffort(decision.Model, true), arm); level != arm {
			return effortResolutionFor(caps, arm, level, effortSourceEscalation)
		}
	}
	if arm != "" {
		return effortResolutionFor(caps, arm, arm, effortSourceArm)
	}
	if escalationAllowed {
		if level := forcedReasoningEffort(decision.Model, false); level != "" {
			return effortResolutionFor(caps, arm, level, effortSourceModelPolicy)
		}
	}
	return effortResolution{}
}

func effortResolutionFor(caps router.ModelSpec, arm, level, source string) effortResolution {
	selected := router.CanonicalizeEffort(level)
	sent := translate.ClampReasoningLevel(caps, translate.ResolveForceEffort(caps, level))
	if sent == "" {
		return effortResolution{Arm: arm}
	}
	return effortResolution{Arm: arm, Selected: selected, Sent: sent, Source: source}
}
