package auth

import (
	"context"

	"weave-os/router/internal/observability"
)

// InstallationBlindExperimentEnabled reports whether installationID has an enabled blind experiment.
// A lookup error counts as enabled so an unidentified subject is never shown routing surfaces; a
// deployment without an experiment repository has no experiments to hide.
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
