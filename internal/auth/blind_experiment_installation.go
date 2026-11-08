package auth

import (
	"context"

	"weave-os/router/internal/observability"
)

// BlindExperimentInstallationReader is implemented by experiment repositories that can answer for an
// installation without a router user. A missing configuration must report false with a nil error.
type BlindExperimentInstallationReader interface {
	InstallationExperimentEnabled(ctx context.Context, installationID string) (bool, error)
}

// InstallationBlindExperimentEnabled reports whether installationID has an enabled blind experiment.
// A lookup error counts as enabled so an unidentified subject is never shown routing surfaces; a
// deployment without an installation-aware experiment repository has no experiments to hide.
func (s *Service) InstallationBlindExperimentEnabled(ctx context.Context, installationID string) bool {
	reader, ok := s.blindExperiments.(BlindExperimentInstallationReader)
	if !ok || installationID == "" {
		return false
	}
	enabled, err := reader.InstallationExperimentEnabled(ctx, installationID)
	if err != nil {
		observability.FromContext(ctx).Warn("Failed to read installation blind experiment", "installation_id", installationID, "err", err)
		return true
	}
	return enabled
}
