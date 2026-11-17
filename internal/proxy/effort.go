package proxy

import (
	"context"
	"strings"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/router"
	"weave-os/router/internal/telemetry"
	"weave-os/router/internal/translate"
)

func applyEffortAttrs(b *telemetry.AttrBuilder, e dispatch.EffortResolution) {
	b.String("routing.arm_effort", e.Arm).
		String("routing.selected_effort", e.Selected).
		String("routing.sent_effort", e.Sent).
		String("routing.effort_source", e.Source).
		Bool("routing.effort_mismatch", e.Mismatch())
}

func (s *Service) resolveEffort(ctx context.Context, decision router.Decision, caps router.ModelSpec, escalate bool) dispatch.EffortResolution {
	arm := router.CanonicalizeEffort(decision.Effort)
	escalationAllowed := s.ResolveEffortEscalation(ctx) || strings.HasPrefix(decision.Model, "grok-")
	if knobs := routingKnobsForRequest(ctx); knobs != nil && knobs.ForceEffort != "" {
		return effortResolutionFor(caps, arm, knobs.ForceEffort, dispatch.EffortSourceUser)
	}
	if escalate && escalationAllowed {
		if level := router.HigherEffort(forcedReasoningEffort(decision.Model, true), arm); level != arm {
			return effortResolutionFor(caps, arm, level, dispatch.EffortSourceEscalation)
		}
	}
	if arm != "" {
		return effortResolutionFor(caps, arm, arm, dispatch.EffortSourceArm)
	}
	if escalationAllowed {
		if level := forcedReasoningEffort(decision.Model, false); level != "" {
			return effortResolutionFor(caps, arm, level, dispatch.EffortSourceModelPolicy)
		}
	}
	return dispatch.EffortResolution{}
}

func effortResolutionFor(caps router.ModelSpec, arm, level, source string) dispatch.EffortResolution {
	selected := router.CanonicalizeEffort(level)
	sent := translate.ClampReasoningLevel(caps, translate.ResolveForceEffort(caps, level))
	if sent == "" {
		return dispatch.EffortResolution{Arm: arm}
	}
	return dispatch.EffortResolution{Arm: arm, Selected: selected, Sent: sent, Source: source}
}
