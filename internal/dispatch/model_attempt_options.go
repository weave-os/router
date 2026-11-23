package dispatch

import (
	"context"

	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"
)

func SwitchedAttemptOptions(ctx context.Context, base translate.EmitOptions, target router.Decision, escalate bool,
	resolveEffort func(context.Context, router.Decision, router.ModelSpec, bool) EffortResolution,
) (translate.EmitOptions, EffortResolution) {
	base.TargetModel = target.Model
	base.TargetProvider = target.Provider
	base.Capabilities = router.Lookup(target.Model)
	base.ModelSwitched = true
	effort := resolveEffort(ctx, target, base.Capabilities, escalate)
	effort.Apply(&base)
	return base, effort
}
