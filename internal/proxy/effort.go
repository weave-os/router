package proxy

import (
	"context"
	"strings"

	"weave-os/router/internal/router"
	"weave-os/router/internal/telemetry"
	"weave-os/router/internal/translate"
)

const (
	effortSourceUser        = "user"
	effortSourceEscalation  = "escalation"
	effortSourceArm         = "arm"
	effortSourceModelPolicy = "model_policy"
)

type effortResolution struct {
	Arm      string
	Selected string
	Sent     string
	Source   string
}

func (e effortResolution) Mismatch() bool {
	if e.Arm != "" {
		return e.Arm != e.Sent
	}
	return e.Selected != "" && e.Selected != e.Sent
}

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

func applyEffortAttrs(b *telemetry.AttrBuilder, e effortResolution) {
	b.String("routing.arm_effort", e.Arm).
		String("routing.selected_effort", e.Selected).
		String("routing.sent_effort", e.Sent).
		String("routing.effort_source", e.Source).
		Bool("routing.effort_mismatch", e.Mismatch())
}

func (s *Service) resolveEffort(ctx context.Context, decision router.Decision, caps router.ModelSpec, escalate bool) effortResolution {
	arm := router.CanonicalizeEffort(decision.Effort)
	escalationAllowed := s.ResolveEffortEscalation(ctx) || strings.HasPrefix(decision.Model, "grok-")
	if knobs := routingKnobsForRequest(ctx); knobs != nil && knobs.ForceEffort != "" {
		return effortResolutionFor(caps, arm, knobs.ForceEffort, effortSourceUser)
	}
	if escalate && escalationAllowed {
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
