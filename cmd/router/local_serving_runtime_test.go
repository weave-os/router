package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/server"
)

func TestLocalServingReleaseRefusesManagedMode(t *testing.T) {
	_, _, err := buildLocalServingRuntime(context.Background(), server.DeploymentModeManaged, t.TempDir(), nil)
	require.ErrorContains(t, err, "only supported in self-hosted deployment mode")
}

func TestLocalServingReleaseRequiresAPrivateWorkerOrigin(t *testing.T) {
	t.Setenv("ROUTER_CLASSIFIER_WORKER_URL", "http://classifier-worker:8093/v5/classify")
	_, _, err := buildLocalServingRuntime(context.Background(), server.DeploymentModeSelfHosted, t.TempDir(), nil)
	require.ErrorContains(t, err, "invalid self-hosted classifier worker origin")
}
