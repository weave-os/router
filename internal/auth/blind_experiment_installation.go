package auth

import (
	"context"

	"weave-os/router/internal/observability"
)

func (s *Service) InstallationBlindExperimentEnabled(ctx context.Context, installationID string) bool {
	if s.blindExperiments == nil {
		return false
	}
	enabled, err := s.blindExperiments.InstallationExperimentEnabled(ctx, installationID)
	if err != nil {
		observability.FromContext(ctx).Warn("Failed to read installation blind experiment", "installation_id", installationID, "err", err)
		return true
	}
	return enabled
}
