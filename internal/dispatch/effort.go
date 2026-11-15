package dispatch

import "weave-os/router/internal/translate"

// Effort sources, ordered by the precedence resolveEffort applies.
const (
	EffortSourceUser        = "user"
	EffortSourceEscalation  = "escalation"
	EffortSourceArm         = "arm"
	EffortSourceModelPolicy = "model_policy"
)

// EffortResolution is one dispatch target's effort: the arm's own level, the
// level that won precedence, and the level the emit path actually writes on
// the wire, which differ when an override outranks the arm or the target's
// menu can't express the selection (xhigh → max → high).
type EffortResolution struct {
	Arm      string
	Selected string
	Sent     string
	Source   string
}

// Mismatch reports whether the wire level differs from the one the policy arm
// is labelled with. An override or a clamp credits that arm with a level it
// never bought, so such a turn must not train the policy.
func (e EffortResolution) Mismatch() bool {
	if e.Arm != "" {
		return e.Arm != e.Sent
	}
	return e.Selected != "" && e.Selected != e.Sent
}

// Apply writes the resolution onto emit options. ForceEffort carries the
// pre-cap level (the Anthropic adaptive seam applies its own cap); the
// escalation and model-policy levels are wire levels only.
func (e EffortResolution) Apply(opts *translate.EmitOptions) {
	switch e.Source {
	case EffortSourceUser, EffortSourceArm:
		opts.ForceEffort = e.Selected
		opts.ForceReasoningEffort = translate.ResolveForceEffort(opts.Capabilities, e.Selected)
	case EffortSourceEscalation, EffortSourceModelPolicy:
		opts.ForceEffort = ""
		opts.ForceReasoningEffort = e.Sent
	default:
		opts.ForceEffort = ""
		opts.ForceReasoningEffort = ""
	}
}
