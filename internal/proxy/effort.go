package proxy

import (
	"context"
	"strings"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/router"
	"weave-os/router/internal/telemetry"
	"weave-os/router/internal/translate"
)

const (
	EffortSourceUser        = dispatch.EffortSourceUser
	EffortSourceEscalation  = dispatch.EffortSourceEscalation
	EffortSourceArm         = dispatch.EffortSourceArm
	EffortSourceModelPolicy = dispatch.EffortSourceModelPolicy
)

type effortResolution = dispatch.EffortResolution

// applyEffortAttrs stamps the effort actually dispatched with, alongside the
// level routing selected and where it came from.
func applyEffortAttrs(b *telemetry.AttrBuilder, e effortResolution) *telemetry.AttrBuilder {
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
		return effortResolutionFor(caps, arm, knobs.ForceEffort, EffortSourceUser)
	}
	if escalate && escalationAllowed {
		// Escalation rescues a turn the arm's level already failed, so it may
		// only raise the level: the per-model escalation default is a fixed
		// "high"/"low" that would otherwise downgrade a richer arm.
		if level := router.HigherEffort(forcedReasoningEffort(decision.Model, true), arm); level != arm {
			return effortResolutionFor(caps, arm, level, EffortSourceEscalation)
		}
	}
	if arm != "" {
		return effortResolutionFor(caps, arm, arm, EffortSourceArm)
	}
	if escalationAllowed {
		if level := forcedReasoningEffort(decision.Model, false); level != "" {
			return effortResolutionFor(caps, arm, level, EffortSourceModelPolicy)
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
