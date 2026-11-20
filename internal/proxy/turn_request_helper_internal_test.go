package proxy

import (
	"context"

	"github.com/google/uuid"

	"weave-os/router/internal/router"
)

func testTurnRequest(ctx context.Context, apiKeyID string, installationID uuid.UUID) turnRequest {
	return turnRequest{Strategy: router.StrategyFromContext(ctx), APIKeyID: apiKeyID, InstallationID: installationID}
}
