package postgres

import (
	"context"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
)

func servableControlModel(ctx context.Context, installationID, controlModel string) string {
	if controlModel == "" {
		return ""
	}
	_, known := catalog.ByID(controlModel)
	if known && !router.IsRoutingModel(controlModel) {
		return controlModel
	}
	observability.FromContext(ctx).Error("Blind experiment control model is not a servable catalog model; falling back to the detected baseline", "installation_id", installationID, "control_model", controlModel)
	return ""
}
