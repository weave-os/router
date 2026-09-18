package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestServingCommandsRejectUnsafeTargetsBeforeOpeningRegistry(t *testing.T) {
	for _, target := range []string{"beta", "prod/beta", "prod/unknown", ""} {
		err := run(context.Background(), []string{"serving", "status", "--target", target})
		require.ErrorContains(t, err, "unsupported managed target")
	}
}

func TestServingActivationIsNotExposedWithoutValidation(t *testing.T) {
	err := run(context.Background(), []string{"serving", "activate"})
	require.ErrorContains(t, err, "unsupported serving command")
}
