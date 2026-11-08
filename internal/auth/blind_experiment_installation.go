package auth

import (
	"context"

	"weave-os/router/internal/observability"
)

// BlindExperimentInstallationReader reports whether an installation has an enabled blind experiment.
type BlindExperimentInstallationReader interface {
	InstallationExperimentEnabled(ctx context.Context, installationID string) (bool, error)
}

// InstallationBlindExperimentEnabled reports whether installationID has an enabled blind experiment.
// It fails open to false when the repository cannot answer.
func (s *Service) InstallationBlindExperimentEnabled(ctx context.Context, installationID string) bool {
	reader, ok := s.blindExperiments.(BlindExperimentInstallationReader)
	if !ok || installationID == "" {
		return false
	}
	enabled, err := reader.InstallationExperimentEnabled(ctx, installationID)
	if err != nil {
		observability.FromContext(ctx).Warn("Failed to read installation blind experiment", "installation_id", installationID, "err", err)
		return false
	}
	return enabled
}
