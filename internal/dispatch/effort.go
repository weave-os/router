package dispatch

import "weave-os/router/internal/translate"

const (
	EffortSourceUser        = "user"
	EffortSourceEscalation  = "escalation"
	EffortSourceArm         = "arm"
	EffortSourceModelPolicy = "model_policy"
)

type EffortResolution struct {
	Arm      string
	Selected string
	Sent     string
	Source   string
}

func (e EffortResolution) Mismatch() bool {
	if e.Arm != "" {
		return e.Arm != e.Sent
	}
	return e.Selected != "" && e.Selected != e.Sent
}

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
