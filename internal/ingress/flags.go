package ingress

import (
	"context"

	"weave-os/router/internal/flags"
)

func (f Formats) ResolveAllowedModelsHeader(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyAllowedModelsHeader, f.AllowedModelsHeader)
}

func (f Formats) ResponsesBroad(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyOpenAIResponsesBroad, f.OpenAIResponsesBroad)
}

func (f Formats) TaskToolsCrossVendor(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyCCTaskToolsCrossVendor, f.CCTaskToolsCrossVendor)
}

func (f Formats) AutonomySystemAppend(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyCCAutonomySystemAppend, f.CCAutonomySystemAppend)
}

func (f Formats) WorkspaceSystemAppend(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyCCWorkspaceSystemAppend, f.CCWorkspaceSystemAppend)
}
