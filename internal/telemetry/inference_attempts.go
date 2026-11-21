package telemetry

import (
	"context"

	"weave-os/router/internal/inference"
)

type InferenceAttemptStore interface {
	InsertInferenceAttempt(ctx context.Context, p InsertInferenceAttemptParams) error
}

type InsertInferenceAttemptParams struct {
	InstallationID string
	Event          inference.AttemptEvent
}
