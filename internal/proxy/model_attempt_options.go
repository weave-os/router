package proxy

import (
	"context"

	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"
)

func switchedAttemptOptions(ctx context.Context, base translate.EmitOptions, target router.Decision, escalate bool,
	resolveEffort func(context.Context, router.Decision, router.ModelSpec, bool) effortResolution,
) (translate.EmitOptions, effortResolution) {
	base.TargetModel = target.Model
	base.TargetProvider = target.Provider
	base.Capabilities = router.Lookup(target.Model)
	base.ModelSwitched = true
	effort := resolveEffort(ctx, target, base.Capabilities, escalate)
	effort.Apply(&base)
	return base, effort
}
