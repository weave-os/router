package middleware

import (
	"context"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/requestcontext"
)

func withInstallationDisplaySettings(ctx context.Context, installation *auth.Installation) context.Context {
	if installation.HideTerminalSurfaces {
		ctx = context.WithValue(ctx, requestcontext.InstallationHideTerminalSurfacesContextKey{}, true)
	}
	if installation.ServingReleaseHeadersEnabled {
		ctx = context.WithValue(ctx, requestcontext.InstallationServingReleaseHeadersContextKey{}, true)
	}
	if installation.ShowModelSelectionReasoning {
		ctx = context.WithValue(ctx, requestcontext.InstallationShowModelSelectionReasoningContextKey{}, true)
	}
	return ctx
}
